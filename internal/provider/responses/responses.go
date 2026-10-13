package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
	"github.com/jack-work/figaro/internal/wirelog"
)

type IDs struct {
	Task        string
	Session     string
	Interaction string
	Machine     string
}

type Config struct {
	Namespace string
	BaseURL   func(token string) string
	Headers   func(token string, ids IDs) http.Header
	Envelope  func(ids IDs) map[string]string
}

type TokenSource interface {
	Resolve() (string, error)
	Invalidate(string) error
}

type responseDialer func(context.Context, string, http.Header) (*websocket.Conn, error)

type Transport int

const (
	Socket Transport = iota
	HTTP
)

type responseFrames func(context.Context) ([]byte, error)

type Provider struct {
	tokenSrc  TokenSource
	cacheOpen func(string) (store.Log[[]json.RawMessage], error)
	namespace string
	headers   func(string, IDs) http.Header
	envelope  func(IDs) map[string]string

	mu        sync.Mutex
	model     string
	maxTokens int
	templates *template.Template
	machineID string
	cache     store.Log[[]json.RawMessage]
	sessionID string
	limits    map[string]ContextLimits
	transport map[string]Transport

	baseURL func(string) string
	dial    responseDialer
}

func New(
	knobs provider.Knobs,
	tokenSrc TokenSource,
	cfg Config,
	cacheOpen func(string) (store.Log[[]json.RawMessage], error),
) *Provider {
	headers := cfg.Headers
	if headers == nil {
		headers = func(token string, _ IDs) http.Header { return BearerHeaders(token) }
	}
	return &Provider{
		tokenSrc:  tokenSrc,
		cacheOpen: cacheOpen,
		namespace: cfg.Namespace,
		headers:   headers,
		envelope:  cfg.Envelope,
		model:     knobs.Model,
		maxTokens: knobs.MaxTokens,
		machineID: uuid.NewString(),
		limits:    map[string]ContextLimits{},
		transport: map[string]Transport{},
		baseURL:   cfg.BaseURL,
		dial:      dialResponses,
	}
}

func BearerHeaders(token string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	return headers
}

func (p *Provider) Namespace() string { return p.namespace }

func (p *Provider) CacheOpen(aria string) (store.Log[[]json.RawMessage], error) {
	return p.cacheOpen(aria)
}

func (p *Provider) SetModel(model string) {
	p.mu.Lock()
	p.model = model
	p.mu.Unlock()
}

func (p *Provider) SetTemplates(templates *template.Template) {
	p.mu.Lock()
	p.templates = templates
	p.mu.Unlock()
}

func (p *Provider) SetContextLimits(model string, limits ContextLimits) {
	if model == "" {
		return
	}
	p.mu.Lock()
	p.limits[model] = limits
	p.mu.Unlock()
}

func (p *Provider) SetTransport(model string, transport Transport) {
	if model == "" {
		return
	}
	p.mu.Lock()
	p.transport[model] = transport
	p.mu.Unlock()
}

func (p *Provider) transportFor(model string) Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.transport[model]
}

func (p *Provider) Fingerprint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fingerprintFor(p.model)
}

func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if model, ok, err := responseOptionalString(in.Snapshot, "system.model"); err != nil {
		return err
	} else if ok && model != "" {
		p.SetModel(model)
	}
	options, err := responseOptionsFor(in.Snapshot)
	if err != nil {
		return err
	}
	model, _, _ := p.settings()
	if err := validateCapabilities(model, options); err != nil {
		return err
	}
	if err := p.validateContext(in, model, options); err != nil {
		return err
	}
	token, err := p.tokenSrc.Resolve()
	if err != nil {
		return fmt.Errorf("responses: resolve token: %w", err)
	}

	err = p.sendWithToken(ctx, token, in, bus, options)
	if err == nil || !isResponseUnauthorized(err) {
		return err
	}
	if ierr := p.tokenSrc.Invalidate(token); ierr != nil {
		return fmt.Errorf("responses: invalidate token: %w", ierr)
	}
	token, err = p.tokenSrc.Resolve()
	if err != nil {
		return fmt.Errorf("responses: resolve refreshed token: %w", err)
	}
	return p.sendWithToken(ctx, token, in, bus, options)
}

func (p *Provider) sendWithToken(
	ctx context.Context,
	token string,
	in provider.SendInput,
	bus provider.Bus,
	options responseRequestOptions,
) (sendErr error) {
	input, err := p.inputFor(in)
	if err != nil {
		return err
	}
	if len(input) == 0 {
		return fmt.Errorf("responses: empty context")
	}
	input, breakpoints := markPromptCacheBreakpoint(input)

	model, maxTokens, machineID := p.settings()
	if model == "" {
		return fmt.Errorf("responses: model is required")
	}
	if in.MaxTokens > 0 {
		maxTokens = in.MaxTokens
	}
	taskID := uuid.NewString()
	sessionID := p.sessionIDFor()
	interactionID := uuid.NewString()
	base := p.baseURL(token)
	ids := IDs{Task: taskID, Session: sessionID, Interaction: interactionID, Machine: machineID}
	headers := p.headers(token, ids)
	body := responseBody{
		Input:             input,
		Instructions:      responseInstructions(in.Snapshot),
		Model:             model,
		ParallelToolCalls: options.parallelToolCalls,
		Reasoning:         options.reasoning,
		Store:             false,
		Temperature:       options.temperature,
		Text:              options.text,
		TopP:              options.topP,
		Tools:             responseTools(in.Tools),
		PromptCacheKey:    provider.SessionKey(in.AriaID),
	}
	if maxTokens > 0 {
		body.MaxOutputTokens = maxTokens
	}

	transport := p.transportFor(model)
	method, endpoint := wirelog.StreamMethodWS, responsesEndpoint(base)
	if transport == HTTP {
		method, endpoint = wirelog.StreamMethodSSE, responsesHTTPEndpoint(base)
	}
	trace := wirelog.BeginStream(ctx, in.AriaID, method, endpoint)
	terminalStatus := ""
	defer func() { trace.Finish(terminalStatus, sendErr) }()

	var frames responseFrames
	if transport == HTTP {
		payload, err := json.Marshal(responseStreamRequest{responseBody: body, Stream: true})
		if err != nil {
			return fmt.Errorf("responses: encode request: %w", err)
		}
		trace.RequestBytes(int64(len(payload)))
		stream, err := postResponses(ctx, endpoint, headers, payload)
		if err != nil {
			return fmt.Errorf("responses: post: %w", err)
		}
		defer stream.Close()
		frames = sseFrames(stream)
	} else {
		conn, err := p.dial(ctx, endpoint, headers)
		if err != nil {
			return fmt.Errorf("responses: dial: %w", err)
		}
		defer conn.CloseNow()
		create := responseCreateRequest{Type: "response.create", responseBody: body}
		if p.envelope != nil {
			create.AgentTaskID = taskID
			create.Headers = p.envelope(ids)
			create.Initiator = "user"
		}
		payload, err := json.Marshal(create)
		if err != nil {
			return fmt.Errorf("responses: encode create: %w", err)
		}
		trace.RequestBytes(int64(len(payload)))
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			return fmt.Errorf("responses: send create: %w", err)
		}
		frames = socketFrames(conn)
	}

	response, err := readResponseStream(ctx, frames, bus, trace)
	terminalStatus = response.Status
	if err != nil {
		// An interrupted stream hands over what it received: completed calls
		// may already have executed.
		if len(response.Output) == 0 {
			return err
		}
		partial, derr := decodeResponseAssistant(response)
		if derr != nil || len(partial.Content) == 0 {
			return err
		}
		partial.StopReason = message.StopAborted
		partial.Timestamp = time.Now().UnixMilli()
		p.handOver(partial, response.Output, bus)
		return err
	}
	assistant, err := decodeResponseAssistant(response)
	if err != nil {
		return err
	}
	logPromptCacheEconomics(response.Usage, breakpoints)
	if len(assistant.Content) == 0 && len(response.Output) == 0 {
		slog.Warn("responses: empty completion, nothing to render",
			"model", model, "status", response.Status,
			"input_tokens", response.Usage.InputTokens,
			"output_tokens", response.Usage.OutputTokens,
			"reasoning_tokens", response.Usage.OutputTokensDetails.ReasoningTokens)
		return nil
	}
	assistant.Timestamp = time.Now().UnixMilli()
	p.handOver(assistant, response.Output, bus)
	return nil
}

// handOver is the single path to the bus for whole and partial messages;
// the fig IR side appends, since only it has the LT.
func (p *Provider) handOver(assistant message.Message, output []json.RawMessage, bus provider.Bus) {
	bus.PushMessageEnd(string(assistant.StopReason))
	var commit []provider.AssistantCache
	if len(output) > 0 {
		commit = append(commit, provider.AssistantCache{
			Namespace:   p.namespace,
			Payload:     output,
			Fingerprint: p.Fingerprint(),
		})
	}
	bus.PushFigaro(assistant, commit...)
}

func (p *Provider) settings() (string, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model, p.maxTokens, p.machineID
}

type ContextLimits struct {
	Default int
	Long    int
}

func (p *Provider) validateContext(
	in provider.SendInput,
	model string,
	options responseRequestOptions,
) error {
	requestedLimit, limitSet, err := responseOptionalInt(in.Snapshot, "system.max_context_tokens")
	if err != nil {
		return err
	}
	if limitSet && requestedLimit <= 0 {
		return fmt.Errorf("responses: system.max_context_tokens must be greater than 0")
	}

	p.mu.Lock()
	limits := p.limits[model]
	p.mu.Unlock()
	tierLimit := limits.Default
	if options.contextTier == "long_context" && limits.Long > 0 {
		tierLimit = limits.Long
	}
	if requestedLimit > 0 {
		if tierLimit > 0 && requestedLimit > tierLimit {
			return fmt.Errorf(
				"responses: system.max_context_tokens %d exceeds the %s limit of %d for %q",
				requestedLimit,
				contextTierName(options.contextTier),
				tierLimit,
				model,
			)
		}
		tierLimit = requestedLimit
	}
	if !limitSet && tierLimit == 0 {
		return nil
	}

	used, _ := contextSizeForLog(in.FigLog)
	if used > tierLimit {
		return fmt.Errorf(
			"responses: estimated prompt context %d tokens exceeds the %s limit of %d for %q; compact the aria or set system.context_tier to \"long_context\"",
			used,
			contextTierName(options.contextTier),
			tierLimit,
			model,
		)
	}
	return nil
}

func contextSizeForLog(log store.Log[message.Message]) (int, bool) {
	const tailSize = 64
	tail := store.TailSnapshot(log, tailSize)
	for i := len(tail) - 1; i >= 0; i-- {
		entry := tail[i]
		if entry.Payload.Usage == nil {
			continue
		}
		usage := entry.Payload.Usage
		total := usage.InputTokens + usage.OutputTokens
		for j := i + 1; j < len(tail); j++ {
			total += tokens.EstimateMessage(tail[j].Payload)
		}
		return total, i == len(tail)-1
	}
	if len(tail) < tailSize {
		total := 0
		for _, entry := range tail {
			total += tokens.EstimateMessage(entry.Payload)
		}
		return total, len(tail) == 0
	}

	// Cold path: Read() rather than a snapshot, so the rare path pays a
	// re-read instead of keeping the whole log resident for the common one.
	entries := log.Read()
	watermark := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Payload.Usage != nil {
			watermark = i
			break
		}
	}
	if watermark < 0 {
		total := 0
		for _, entry := range entries {
			total += tokens.EstimateMessage(entry.Payload)
		}
		return total, false
	}
	usage := entries[watermark].Payload.Usage
	total := usage.InputTokens + usage.OutputTokens
	for _, entry := range entries[watermark+1:] {
		total += tokens.EstimateMessage(entry.Payload)
	}
	return total, watermark == len(entries)-1
}

func contextTierName(tier string) string {
	if tier == "long_context" {
		return "long-context"
	}
	return "default-context"
}

func (p *Provider) sessionIDFor() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionID != "" {
		return p.sessionID
	}
	p.sessionID = uuid.NewString()
	return p.sessionID
}

func (p *Provider) cacheFor(aria string) (store.Log[[]json.RawMessage], error) {
	if aria == "" || p.cacheOpen == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fingerprint := p.fingerprintFor(p.model)
	if p.cache != nil {
		if !p.invalidateCache(p.cache, fingerprint) {
			return nil, fmt.Errorf("responses cache invalidation failed for %s", aria)
		}
		return p.cache, nil
	}
	cache, err := p.cacheOpen(aria)
	if err != nil {
		return nil, fmt.Errorf("responses open translator log: %w", err)
	}
	if !p.invalidateCache(cache, fingerprint) {
		return nil, fmt.Errorf("responses translator log invalidation failed")
	}
	p.cache = cache
	return cache, nil
}

func (p *Provider) invalidateCache(cache store.Log[[]json.RawMessage], fingerprint string) bool {
	_, _, err := provider.ClearStaleTranslationCache(cache, fingerprint)
	return err == nil
}

func (p *Provider) inputFor(in provider.SendInput) ([]json.RawMessage, error) {
	rows, err := p.cacheFor(in.AriaID)
	if err != nil {
		return nil, err
	}
	templates := p.templatesForEncoding()

	if _, err := provider.CatchUp(provider.CatchUpConfig{
		Log:         in.FigLog,
		Translator:  rows,
		Form:        in.Form,
		Studies:     in.Studies,
		Fingerprint: p.Fingerprint(),
		Provider:    p.Namespace(),
		Encode: func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			encoded, err := encodeResponseMessage(msg, msg.Patches, snap, templates)
			if err != nil {
				return nil, fmt.Errorf("responses: encode message %d: %w", msg.LogicalTime, err)
			}
			for _, text := range append(provider.StudyReminderTexts(msg, snap),
				provider.ForkReminderTexts(msg, snap)...) {
				part, merr := json.Marshal(map[string]any{
					"role":    "user",
					"content": []map[string]any{{"type": "input_text", "text": text}},
				})
				if merr == nil {
					encoded = append(encoded, part)
				}
			}
			return encoded, nil
		},
		ReportWriteError: func(lt uint64, err error) {
			slog.Error("responses write row", "aria", in.AriaID, "lt", lt, "err", err)
		},
	}); err != nil {
		// A send that cannot write its rows must not proceed.
		return nil, fmt.Errorf("responses catch up: %w", err)
	}

	// The websocket frame is a typed array: one array, walked once.
	to, ok := provider.TranslationTail(rows)
	if !ok {
		return nil, nil
	}
	var input []json.RawMessage
	for row := range provider.TranslationRows(rows, to) {
		input = append(input, row)
	}
	return input, nil
}

func (p *Provider) templatesForEncoding() *template.Template {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.templates
}

func responsesEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasPrefix(baseURL, "https://") {
		return "wss://" + strings.TrimPrefix(baseURL, "https://") + "/responses"
	}
	if strings.HasPrefix(baseURL, "http://") {
		return "ws://" + strings.TrimPrefix(baseURL, "http://") + "/responses"
	}
	return baseURL + "/responses"
}

func responsesHTTPEndpoint(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/responses"
}

func socketFrames(conn *websocket.Conn) responseFrames {
	return func(ctx context.Context) ([]byte, error) {
		_, raw, err := conn.Read(ctx)
		return raw, err
	}
}

const maxResponseFrame = 32 << 20

func postResponses(ctx context.Context, endpoint string, headers http.Header, payload []byte) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, httpResponseRejection(resp.StatusCode, raw)
	}
	return resp.Body, nil
}

func httpResponseRejection(status int, body []byte) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	var rejected *responseAPIError
	if !errors.As(responseFailure(envelope.Error), &rejected) {
		rejected = &responseAPIError{code: "provider_error"}
	}
	if rejected.code == "provider_error" {
		rejected.code = statusRejectionCode(status, "provider_error")
	}
	rejected.status = status
	rejected.retryable = status == http.StatusUnauthorized
	return rejected
}

func statusRejectionCode(status int, fallback string) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "permission_denied"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	}
	return fallback
}

func sseFrames(r io.Reader) responseFrames {
	reader := bufio.NewReaderSize(r, 64<<10)
	finished := false
	return func(context.Context) ([]byte, error) {
		if finished {
			return nil, io.EOF
		}
		var data []byte
		have := false
		for {
			line, err := readSSELine(reader)
			if err != nil && err != io.EOF {
				return nil, err
			}
			atEOF := err == io.EOF
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
			case line[0] == ':':
			default:
				field, value, _ := bytes.Cut(line, []byte(":"))
				if string(field) == "data" {
					if have {
						data = append(data, '\n')
					}
					data = append(data, bytes.TrimPrefix(value, []byte(" "))...)
					have = true
					if len(data) > maxResponseFrame {
						return nil, fmt.Errorf("server-sent event exceeds %d bytes", maxResponseFrame)
					}
				}
			}
			if have && (len(line) == 0 || atEOF) {
				if string(data) == "[DONE]" {
					finished = true
					return nil, io.EOF
				}
				finished = atEOF
				return data, nil
			}
			if atEOF {
				finished = true
				return nil, io.EOF
			}
		}
	}
}

func readSSELine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxResponseFrame {
			return nil, fmt.Errorf("server-sent event line exceeds %d bytes", maxResponseFrame)
		}
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

func dialResponses(ctx context.Context, endpoint string, headers http.Header) (*websocket.Conn, error) {
	headers = headers.Clone()
	headers.Set("Origin", "https://github.com")
	client := &http.Client{
		Timeout: 30 * time.Second,
		// A redirect must not forward the credential to another endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	conn, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient: client, HTTPHeader: headers,
	})
	if err != nil {
		if response != nil {
			code := statusRejectionCode(response.StatusCode, "handshake_rejected")
			return nil, &responseAPIError{code: code, status: response.StatusCode, retryable: response.StatusCode == http.StatusUnauthorized}
		}
		return nil, err
	}
	// The library default of 32 KiB is smaller than a tool's argument payload.
	conn.SetReadLimit(maxResponseFrame)
	return conn, nil
}

type responseCreateRequest struct {
	Type        string            `json:"type"`
	AgentTaskID string            `json:"agent_task_id,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Initiator   string            `json:"initiator,omitempty"`
	responseBody
}

type responseStreamRequest struct {
	responseBody
	Stream bool `json:"stream"`
}

type responseBody struct {
	Input             []json.RawMessage  `json:"input"`
	Instructions      string             `json:"instructions,omitempty"`
	MaxOutputTokens   int                `json:"max_output_tokens,omitempty"`
	Model             string             `json:"model"`
	ParallelToolCalls bool               `json:"parallel_tool_calls"`
	Reasoning         *responseReasoning `json:"reasoning,omitempty"`
	Store             bool               `json:"store"`
	Temperature       *float64           `json:"temperature,omitempty"`
	Text              *responseText      `json:"text,omitempty"`
	TopP              *float64           `json:"top_p,omitempty"`
	Tools             []responseTool     `json:"tools,omitempty"`
	PromptCacheKey    string             `json:"prompt_cache_key,omitempty"`
}

type responseReasoning struct {
	Context string `json:"context,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responseText struct {
	Verbosity string `json:"verbosity"`
}

type responseRequestOptions struct {
	contextTier       string
	parallelToolCalls bool
	reasoning         *responseReasoning
	temperature       *float64
	text              *responseText
	topP              *float64
}

type responseTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
	Strict      bool   `json:"strict"`
}

func responseTools(tools []provider.Tool) []responseTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responseTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, responseTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return out
}

func decodeResponseArguments(raw string) (map[string]interface{}, error) {
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return nil, err
	}
	if arguments == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return arguments, nil
}

func encodeResponseMessage(
	msg message.Message,
	patches []message.Patch,
	snap form.Snapshot,
	templates *template.Template,
) ([]json.RawMessage, error) {
	var beforeMessage []json.RawMessage
	var afterMessage []json.RawMessage
	var userContent []responseContent
	var assistantContent []responseContent
	spoke := false
	lastOutput, lastCall, lastText := -1, "", ""

	for _, content := range msg.Content {
		switch msg.Role {
		case message.RoleInput:
			switch content.Type {
			case message.ContentProse:
				if content.Text != "" {
					spoke = true
					userContent = append(userContent, responseContent{Type: "input_text", Text: content.Text})
				}
			case message.ContentImage:
				if content.Data != "" {
					spoke = true
					// A tool's image trails its function_call_output in a user
					// message, captioned with the call; the Copilot proxy is
					// unverified for array-shaped output.
					if content.ToolCallID != "" {
						userContent = append(userContent, responseContent{
							Type: "input_text",
							Text: toolImageCaption(content),
						})
					}
					userContent = append(userContent, responseContent{
						Type:     "input_image",
						ImageURL: "data:" + content.MimeType + ";base64," + content.Data,
					})
				}
			case message.ContentToolResult:
				raw, err := marshalResponseItem(responseFunctionOutput(content.ToolCallID, content.Text))
				if err != nil {
					return nil, err
				}
				beforeMessage = append(beforeMessage, raw)
				lastOutput, lastCall, lastText = len(beforeMessage)-1, content.ToolCallID, content.Text
			}
		case message.RoleOutput:
			switch content.Type {
			case message.ContentProse:
				if content.Text != "" {
					assistantContent = append(assistantContent, responseContent{Type: "output_text", Text: content.Text})
				}
			case message.ContentToolInvoke:
				raw, err := marshalResponseItem(responseFunctionCall(content))
				if err != nil {
					return nil, err
				}
				afterMessage = append(afterMessage, raw)
			}
		case message.RoleSystem:
			if content.Type == message.ContentProse && content.Text != "" {
				userContent = append(userContent, responseContent{Type: "input_text", Text: content.Text})
			}
		case message.RoleSystemInterrupt:
			if content.Type == message.ContentInterrupt {
				raw, err := marshalResponseItem(responseFunctionOutput(content.ToolCallID, content.Text))
				if err != nil {
					return nil, err
				}
				beforeMessage = append(beforeMessage, raw)
			}
		}
	}

	var reminders []string
	for _, patch := range patches {
		rendered, err := renderResponsePatch(patch, snap, templates)
		if err != nil {
			return nil, err
		}
		reminders = append(reminders, rendered...)
		snap = snap.Apply(patch)
	}
	// In a results-only round the reminders ride inside the last
	// function_call_output: a user message of their own reads as an empty prompt.
	if !spoke && lastOutput >= 0 && len(userContent) == 0 && len(reminders) > 0 {
		out := lastText
		for _, r := range reminders {
			out += "\n\n" + r
		}
		raw, err := marshalResponseItem(responseFunctionOutput(lastCall, out))
		if err != nil {
			return nil, err
		}
		beforeMessage[lastOutput] = raw
		reminders = nil
	}
	for _, text := range reminders {
		userContent = append(userContent, responseContent{Type: "input_text", Text: text})
	}

	if len(userContent) > 0 {
		raw, err := marshalResponseItem(responseMessage("user", userContent))
		if err != nil {
			return nil, err
		}
		afterMessage = append([]json.RawMessage{raw}, afterMessage...)
	}
	if len(assistantContent) > 0 && msg.Role == message.RoleOutput {
		raw, err := marshalResponseItem(responseMessage("assistant", assistantContent))
		if err != nil {
			return nil, err
		}
		afterMessage = append([]json.RawMessage{raw}, afterMessage...)
	}
	return append(beforeMessage, afterMessage...), nil
}

type responseContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	// PromptCacheBreakpoint is stamped on the assembled request, never on
	// the per-LT cached bytes: it moves.
	PromptCacheBreakpoint *promptCacheBreakpoint `json:"prompt_cache_breakpoint,omitempty"`
}

type promptCacheBreakpoint struct {
	Type string `json:"type"`
}

type responseInputItem struct {
	Type      string            `json:"type,omitempty"`
	Role      string            `json:"role,omitempty"`
	Content   []responseContent `json:"content,omitempty"`
	CallID    string            `json:"call_id,omitempty"`
	Name      string            `json:"name,omitempty"`
	Arguments string            `json:"arguments,omitempty"`
	Output    string            `json:"output,omitempty"`
}

func responseMessage(role string, content []responseContent) responseInputItem {
	return responseInputItem{Type: "message", Role: role, Content: content}
}

func responseFunctionCall(content message.Content) responseInputItem {
	arguments := "{}"
	if len(content.Arguments) > 0 {
		if raw, err := json.Marshal(content.Arguments); err == nil {
			arguments = string(raw)
		}
	}
	return responseInputItem{
		Type:      "function_call",
		CallID:    content.ToolCallID,
		Name:      content.ToolName,
		Arguments: arguments,
	}
}

func responseFunctionOutput(callID, output string) responseInputItem {
	if output == "" {
		output = "(empty)"
	}
	return responseInputItem{Type: "function_call_output", CallID: callID, Output: output}
}

func marshalResponseItem(item responseInputItem) (json.RawMessage, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func renderResponsePatch(
	patch message.Patch,
	snap form.Snapshot,
	templates *template.Template,
) ([]string, error) {
	if templates == nil {
		return nil, nil
	}
	rendered, err := form.Render(patch, snap, templates)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rendered))
	for _, reminder := range rendered {
		out = append(out, "<system-reminder name=\""+escapeResponseAttr(reminder.Key)+"\">\n"+reminder.Body+"\n</system-reminder>")
	}
	return out, nil
}

func escapeResponseAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return strings.ReplaceAll(value, "<", "&lt;")
}

func responseInstructions(snap form.Snapshot) string {
	raw, ok := snap.Get("system.credo")
	if !ok {
		return ""
	}
	var envelope struct {
		Content     string `json:"content"`
		Frontmatter string `json:"frontmatter"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if envelope.Content != "" {
			return envelope.Content
		}
		if envelope.Frontmatter != "" {
			return envelope.Frontmatter
		}
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func (p *Provider) fingerprintFor(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unset"
	}
	return p.namespace + "/v5/" + model
}

func responseOptionsFor(snap form.Snapshot) (responseRequestOptions, error) {
	options := responseRequestOptions{parallelToolCalls: true}

	if parallel, ok, err := responseOptionalBool(snap, "system.parallel_tool_calls"); err != nil {
		return responseRequestOptions{}, err
	} else if ok {
		options.parallelToolCalls = parallel
	}

	contextTier, _, err := responseOptionalString(snap, "system.context_tier")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if contextTier != "" && contextTier != "default" && contextTier != "long_context" {
		return responseRequestOptions{}, fmt.Errorf("responses: system.context_tier must be \"default\" or \"long_context\", got %q", contextTier)
	}
	options.contextTier = contextTier

	effort, _, err := responseOptionalString(snap, "system.thinking_effort")
	if err != nil {
		return responseRequestOptions{}, err
	}
	reasoningContext, _, err := responseOptionalString(snap, "system.reasoning_context")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if reasoningContext != "" && reasoningContext != "auto" && reasoningContext != "current_turn" && reasoningContext != "all_turns" {
		return responseRequestOptions{}, fmt.Errorf("responses: system.reasoning_context must be \"auto\", \"current_turn\", or \"all_turns\", got %q", reasoningContext)
	}
	reasoningSummary, _, err := responseOptionalString(snap, "system.reasoning_summary")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if reasoningSummary != "" && reasoningSummary != "auto" && reasoningSummary != "concise" && reasoningSummary != "detailed" {
		return responseRequestOptions{}, fmt.Errorf("responses: system.reasoning_summary must be \"auto\", \"concise\", or \"detailed\", got %q", reasoningSummary)
	}
	if reasoningContext != "" || effort != "" || reasoningSummary != "" {
		options.reasoning = &responseReasoning{
			Context: reasoningContext,
			Effort:  effort,
			Summary: reasoningSummary,
		}
	}

	if verbosity, _, err := responseOptionalString(snap, "system.verbosity"); err != nil {
		return responseRequestOptions{}, err
	} else if verbosity != "" {
		options.text = &responseText{Verbosity: verbosity}
	}

	temperature, temperatureSet, err := responseOptionalFloat(snap, "system.temperature")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if temperatureSet && (temperature < 0 || temperature > 2) {
		return responseRequestOptions{}, fmt.Errorf("responses: system.temperature must be between 0 and 2")
	}
	topP, topPSet, err := responseOptionalFloat(snap, "system.top_p")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if topPSet && (topP <= 0 || topP > 1) {
		return responseRequestOptions{}, fmt.Errorf("responses: system.top_p must be greater than 0 and at most 1")
	}
	if temperatureSet && topPSet {
		return responseRequestOptions{}, fmt.Errorf("responses: set either system.temperature or system.top_p, not both")
	}
	if temperatureSet {
		options.temperature = &temperature
	}
	if topPSet {
		options.topP = &topP
	}

	return options, nil
}

func responseOptionalString(snap form.Snapshot, key string) (string, bool, error) {
	raw, ok := snap.Get(key)
	if !ok || string(raw) == "null" {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("responses: %s must be a string: %w", key, err)
	}
	return strings.TrimSpace(value), true, nil
}

func responseOptionalBool(snap form.Snapshot, key string) (bool, bool, error) {
	raw, ok := snap.Get(key)
	if !ok || string(raw) == "null" {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false, fmt.Errorf("responses: %s must be a boolean: %w", key, err)
	}
	return value, true, nil
}

func responseOptionalFloat(snap form.Snapshot, key string) (float64, bool, error) {
	raw, ok := snap.Get(key)
	if !ok || string(raw) == "null" {
		return 0, false, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, fmt.Errorf("responses: %s must be a number: %w", key, err)
	}
	return value, true, nil
}

func responseOptionalInt(snap form.Snapshot, key string) (int, bool, error) {
	raw, ok := snap.Get(key)
	if !ok || string(raw) == "null" {
		return 0, false, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, fmt.Errorf("responses: %s must be an integer: %w", key, err)
	}
	return value, true, nil
}

func decodeResponseAssistant(response responseObject) (message.Message, error) {
	out := message.Message{
		Role: message.RoleOutput,
		// input_tokens includes the cached/written breakdown beside it.
		Usage: provider.UsageFromInclusivePrompt(
			response.Usage.InputTokens,
			response.Usage.InputTokensDetails.CachedTokens,
			response.Usage.InputTokensDetails.CacheWriteTokens,
			response.Usage.OutputTokens,
		),
	}
	for _, raw := range response.Output {
		var item responseOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return message.Message{}, fmt.Errorf("responses: decode output item: %w", err)
		}
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					if content.Text != "" {
						out.Content = append(out.Content, message.Content{Type: message.ContentProse, Text: content.Text})
					}
				case "reasoning", "reasoning_text", "reasoning_summary":
					if content.Text != "" {
						out.Content = append(out.Content, message.Content{Type: message.ContentThinking, Text: content.Text})
					}
				}
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" {
				return message.Message{}, fmt.Errorf("responses: function call missing call_id or name")
			}
			arguments := responseArguments(string(responseArgumentBytes(item.Arguments)))
			out.Content = append(out.Content, message.Content{
				Type:       message.ContentToolInvoke,
				ToolCallID: item.CallID,
				ToolName:   item.Name,
				Arguments:  arguments,
			})
		case "reasoning":
			for _, summary := range item.Summary {
				if summary.Text != "" {
					out.Content = append(out.Content, message.Content{Type: message.ContentThinking, Text: summary.Text})
				}
			}
		}
	}
	if response.Status == "incomplete" {
		out.StopReason = message.StopLength
	} else if hasResponseToolInvoke(out.Content) {
		out.StopReason = message.StopToolInvoke
	} else {
		out.StopReason = message.StopEnd
	}
	return out, nil
}

func hasResponseToolInvoke(content []message.Content) bool {
	for _, block := range content {
		if block.Type == message.ContentToolInvoke {
			return true
		}
	}
	return false
}

func isResponseUnauthorized(err error) bool {
	var rejected *responseAPIError
	if !errors.As(err, &rejected) || !rejected.retryable {
		return false
	}
	return rejected.status == http.StatusUnauthorized || rejected.code == "unauthorized" || rejected.code == "invalid_api_key"
}

// toolImageCaption binds an image to its call across the message boundary.
func toolImageCaption(c message.Content) string {
	if c.ToolName != "" {
		return fmt.Sprintf("[image output of tool %s (call %s)]", c.ToolName, c.ToolCallID)
	}
	return fmt.Sprintf("[image output of call %s]", c.ToolCallID)
}

// markPromptCacheBreakpoint stamps one breakpoint at the end of the last
// completed exchange.
func markPromptCacheBreakpoint(input []json.RawMessage) ([]json.RawMessage, int) {
	for i := len(input) - 1; i >= 0; i-- {
		var item responseInputItem
		if err := json.Unmarshal(input[i], &item); err != nil {
			continue
		}
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		leaf := -1
		for j, c := range item.Content {
			if c.Type == "output_text" || c.Type == "input_text" || c.Text != "" {
				leaf = j
			}
		}
		if leaf < 0 {
			return input, 0
		}
		item.Content[leaf].PromptCacheBreakpoint = &promptCacheBreakpoint{Type: "explicit"}
		raw, err := marshalResponseItem(item)
		if err != nil {
			return input, 0
		}
		out := make([]json.RawMessage, len(input))
		copy(out, input)
		out[i] = raw
		return out, 1
	}
	return input, 0
}

func logPromptCacheEconomics(usage responseUsage, breakpoints int) {
	reads := usage.InputTokensDetails.CachedTokens
	writes := usage.InputTokensDetails.CacheWriteTokens
	if reads == 0 && writes == 0 {
		return
	}
	slog.Debug("responses prompt cache",
		"cache_read_tokens", reads,
		"cache_write_tokens", writes,
		"uncached_input_tokens", usage.InputTokens-reads-writes,
		"explicit_breakpoints", breakpoints)
}
