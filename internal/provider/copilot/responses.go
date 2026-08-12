package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

const responsesFingerprintPrefix = "copilot-responses/v2"

type responseTokenSource interface {
	Resolve() (string, error)
	Invalidate(string) error
}

type responseDialer func(context.Context, string, http.Header) (*websocket.Conn, error)

type responsesProvider struct {
	tokenSrc  responseTokenSource
	cacheOpen func(string) (store.Log[[]json.RawMessage], error)

	mu         sync.Mutex
	model      string
	maxTokens  int
	templates  *template.Template
	machineID  string
	cache      store.Log[[]json.RawMessage]
	projection *provider.IncrementalProjection[[]json.RawMessage]
	sessionID  string
	limits     map[string]responseContextLimits

	baseURL func(string) string
	dial    responseDialer
}

func newResponsesProvider(
	knobs provider.Knobs,
	tokenSrc responseTokenSource,
	enterpriseDomain string,
	cacheOpen func(string) (store.Log[[]json.RawMessage], error),
) *responsesProvider {
	return &responsesProvider{
		tokenSrc:  tokenSrc,
		cacheOpen: cacheOpen,
		model:     knobs.Model,
		maxTokens: knobs.MaxTokens,
		machineID: uuid.NewString(),
		limits:    map[string]responseContextLimits{},
		baseURL: func(token string) string {
			if b, ok := tokenSrc.(interface{ BaseURL() string }); ok {
				if u := b.BaseURL(); u != "" {
					return u
				}
			}
			return baseURLFromToken(token, enterpriseDomain)
		},
		dial: dialResponses,
	}
}

func (p *responsesProvider) SetModel(model string) {
	p.mu.Lock()
	p.model = model
	p.mu.Unlock()
}

func (p *responsesProvider) SetTemplates(templates *template.Template) {
	p.mu.Lock()
	p.templates = templates
	p.mu.Unlock()
}

func (p *responsesProvider) SetContextLimits(model string, limits responseContextLimits) {
	if model == "" {
		return
	}
	p.mu.Lock()
	p.limits[model] = limits
	p.mu.Unlock()
}

func (p *responsesProvider) Fingerprint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return responseFingerprint(p.model)
}

func (p *responsesProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
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
	if err := p.validateContext(in, model, options); err != nil {
		return err
	}
	token, err := p.tokenSrc.Resolve()
	if err != nil {
		return fmt.Errorf("copilot responses: resolve token: %w", err)
	}

	err = p.sendWithToken(ctx, token, in, bus, options)
	if err == nil || !isResponseUnauthorized(err) {
		return err
	}
	if ierr := p.tokenSrc.Invalidate(token); ierr != nil {
		return fmt.Errorf("copilot responses: invalidate token: %w", ierr)
	}
	token, err = p.tokenSrc.Resolve()
	if err != nil {
		return fmt.Errorf("copilot responses: resolve refreshed token: %w", err)
	}
	return p.sendWithToken(ctx, token, in, bus, options)
}

func (p *responsesProvider) sendWithToken(
	ctx context.Context,
	token string,
	in provider.SendInput,
	bus provider.Bus,
	options responseRequestOptions,
) error {
	input, err := p.inputFor(in)
	if err != nil {
		return err
	}
	if len(input) == 0 {
		return fmt.Errorf("copilot responses: empty context")
	}
	input, breakpoints := markPromptCacheBreakpoint(input)

	model, maxTokens, machineID := p.settings()
	if model == "" {
		return fmt.Errorf("copilot responses: model is required")
	}
	if in.MaxTokens > 0 {
		maxTokens = in.MaxTokens
	}
	taskID := uuid.NewString()
	sessionID := p.sessionIDFor()
	interactionID := uuid.NewString()
	endpoint := responsesEndpoint(p.baseURL(token))
	headers := responseHeaders(token, taskID, sessionID, interactionID, machineID)
	conn, err := p.dial(ctx, endpoint, headers)
	if err != nil {
		return fmt.Errorf("copilot responses: dial: %w", err)
	}
	defer conn.Close()

	request := responseCreateRequest{
		Type:              "response.create",
		AgentTaskID:       taskID,
		Headers:           responseRequestHeaders(taskID, sessionID, interactionID, machineID),
		Initiator:         "user",
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
		request.MaxOutputTokens = maxTokens
	}
	if err := websocket.JSON.Send(conn, request); err != nil {
		return fmt.Errorf("copilot responses: send create: %w", err)
	}

	response, err := readResponseStream(ctx, conn, bus)
	if err != nil {
		return err
	}
	assistant, err := decodeResponseAssistant(response)
	if err != nil {
		return err
	}
	logPromptCacheEconomics(response.Usage, breakpoints)
	if len(assistant.Content) == 0 && len(response.Output) == 0 {
		// A turn that produced nothing renders as nothing: no text, no
		// aria, no error. Silence here is indistinguishable from a bug
		// anywhere upstream, and it cost two agents an afternoon.
		slog.Warn("copilot responses: empty completion, nothing to render",
			"model", model, "status", response.Status,
			"input_tokens", response.Usage.InputTokens,
			"output_tokens", response.Usage.OutputTokens,
			"reasoning_tokens", response.Usage.OutputTokensDetails.ReasoningTokens)
		return nil
	}
	assistant.Timestamp = time.Now().UnixMilli()
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: assistant})
	if err != nil {
		return fmt.Errorf("copilot responses: append assistant: %w", err)
	}
	assistant.LogicalTime = entry.LT
	bus.PushMessageEnd(string(assistant.StopReason))
	var commit []provider.AssistantCache
	if len(response.Output) > 0 {
		commit = append(commit, provider.AssistantCache{
			Namespace:   "copilot-responses",
			Payload:     response.Output,
			Fingerprint: p.Fingerprint(),
		})
	}
	bus.PushFigaro(assistant, commit...)
	if len(response.Output) > 0 {
		p.acceptAssistantProjection(entry.LT, response.Output)
	}
	return nil
}

func (p *responsesProvider) acceptAssistantProjection(lt uint64, payload []json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.projection == nil {
		return
	}
	state := append([]json.RawMessage(nil), p.projection.State...)
	state = append(state, payload...)
	p.projection = &provider.IncrementalProjection[[]json.RawMessage]{
		State:           state,
		Form:            p.projection.Form,
		Fingerprint:     p.projection.Fingerprint,
		Entries:         p.projection.Entries + 1,
		LastLT:          lt,
		LastFormVersion: p.projection.LastFormVersion,
	}
}

func (p *responsesProvider) settings() (string, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model, p.maxTokens, p.machineID
}

type responseContextLimits struct {
	Default int
	Long    int
}

func (p *responsesProvider) validateContext(
	in provider.SendInput,
	model string,
	options responseRequestOptions,
) error {
	requestedLimit, limitSet, err := responseOptionalInt(in.Snapshot, "system.max_context_tokens")
	if err != nil {
		return err
	}
	if limitSet && requestedLimit <= 0 {
		return fmt.Errorf("copilot responses: system.max_context_tokens must be greater than 0")
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
				"copilot responses: system.max_context_tokens %d exceeds the %s limit of %d for %q",
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
			"copilot responses: estimated prompt context %d tokens exceeds the %s limit of %d for %q; compact the aria or set system.context_tier to \"long_context\"",
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

	// Cold path: no usage anywhere in the last 64 entries, so find the last
	// one that has any. Read() rather than a zero-copy snapshot, deliberately
	//: under a windowed cache the prefix may not be resident at all, and a
	// rare path should pay a re-read instead of forcing the whole log to stay
	// in memory for the common one.
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

func (p *responsesProvider) sessionIDFor() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionID != "" {
		return p.sessionID
	}
	p.sessionID = uuid.NewString()
	return p.sessionID
}

func (p *responsesProvider) cacheFor(aria string) (store.Log[[]json.RawMessage], error) {
	if aria == "" || p.cacheOpen == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fingerprint := responseFingerprint(p.model)
	if p.cache != nil {
		if !p.invalidateCache(p.cache, fingerprint) {
			return nil, fmt.Errorf("copilot responses cache invalidation failed for %s", aria)
		}
		return p.cache, nil
	}
	cache, err := p.cacheOpen(aria)
	if err != nil {
		slog.Warn("copilot responses cache open failed; running uncached", "aria", aria, "err", err)
		return nil, nil
	}
	if !p.invalidateCache(cache, fingerprint) {
		slog.Warn("copilot responses cache invalidation failed; running uncached", "aria", aria)
		return nil, nil
	}
	p.cache = cache
	return cache, nil
}

func (p *responsesProvider) invalidateCache(cache store.Log[[]json.RawMessage], fingerprint string) bool {
	_, _, err := provider.ClearStaleTranslationCache(cache, fingerprint)
	return err == nil
}

func (p *responsesProvider) inputFor(in provider.SendInput) ([]json.RawMessage, error) {
	cache, err := p.cacheFor(in.AriaID)
	if err != nil {
		return nil, err
	}
	fingerprint := p.Fingerprint()
	templates := p.templatesForEncoding()

	p.mu.Lock()
	previous := p.projection
	p.mu.Unlock()

	projection, _, err := provider.ProjectIncrementally(provider.ProjectionConfig[[]json.RawMessage]{
		Log:         in.FigLog,
		Cache:       cache,
		Form:        in.Form,
		Studies:     in.Studies,
		Previous:    previous,
		Fingerprint: fingerprint,
		Encode: func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			encoded, err := encodeResponseMessage(msg, msg.Patches, snap, templates)
			if err != nil {
				return nil, fmt.Errorf("copilot responses: encode message %d: %w", msg.LogicalTime, err)
			}
			// The observed set folds in beside the board: each study
			// reminder becomes one input_text message part.
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
		Append: func(input, encoded []json.RawMessage, _ uint64) []json.RawMessage {
			return append(input, encoded...)
		},
		HandleCacheError: func(lt uint64, err error) {
			slog.Error("copilot responses cache message", "aria", in.AriaID, "lt", lt, "err", err)
		},
	})
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.projection = projection
	p.mu.Unlock()
	return projection.State, nil
}

func (p *responsesProvider) templatesForEncoding() *template.Template {
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

func dialResponses(ctx context.Context, endpoint string, headers http.Header) (*websocket.Conn, error) {
	config, err := websocket.NewConfig(endpoint, "https://github.com")
	if err != nil {
		return nil, err
	}
	config.Header = headers
	config.Dialer = &net.Dialer{Timeout: 30 * time.Second}
	return config.DialContext(ctx)
}

func responseHeaders(token, taskID, sessionID, interactionID, machineID string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	headers.Set("Openai-Intent", "conversation-edits")
	headers.Set("X-Initiator", "user")
	headers.Set("X-GitHub-Api-Version", copilotAPIVersion)
	headers.Set("X-Agent-Task-Id", taskID)
	headers.Set("X-Client-Machine-Id", machineID)
	headers.Set("X-Client-Session-Id", sessionID)
	headers.Set("X-Github-Repository-Host", "github.com")
	headers.Set("X-Github-Repository-Nwo", "")
	headers.Set("X-Interaction-Id", interactionID)
	headers.Set("X-Interaction-Type", "user")
	for key, value := range copilotStaticHeaders {
		headers.Set(key, value)
	}
	return headers
}

func responseRequestHeaders(taskID, sessionID, interactionID, machineID string) map[string]string {
	return map[string]string{
		"X-Agent-Task-Id":          taskID,
		"X-Client-Machine-Id":      machineID,
		"X-Client-Session-Id":      sessionID,
		"X-Interaction-Id":         interactionID,
		"X-Interaction-Type":       "user",
		"X-Github-Repository-Host": "github.com",
		"X-Github-Repository-Nwo":  "",
	}
}

type responseCreateRequest struct {
	Type              string             `json:"type"`
	AgentTaskID       string             `json:"agent_task_id"`
	Headers           map[string]string  `json:"headers"`
	Initiator         string             `json:"initiator"`
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
	// PromptCacheKey pins cache routing for a conversation. OpenAI states
	// it must be set on GPT-5.6 and later to get the reliable cache
	// matching; without it, requests are routed by a hash of roughly the
	// first 256 tokens and a conversation can drift onto a machine holding
	// no cache for it.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
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
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
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

	for _, content := range msg.Content {
		switch msg.Role {
		case message.RoleInput:
			switch content.Type {
			case message.ContentProse:
				if content.Text != "" {
					userContent = append(userContent, responseContent{Type: "input_text", Text: content.Text})
				}
			case message.ContentImage:
				if content.Data != "" {
					// A tool's image trails its function_call_output in the user
					// message this loop emits after it, captioned with the call.
					// OpenAI has since allowed an array-shaped output that could
					// hold the image directly, but the Copilot proxy in front of
					// this endpoint is unverified for it and a rejected item
					// fails the whole turn; this shape only uses items the
					// encoder already sends today.
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

	for _, patch := range patches {
		rendered, err := renderResponsePatch(patch, snap, templates)
		if err != nil {
			return nil, err
		}
		for _, text := range rendered {
			userContent = append(userContent, responseContent{Type: "input_text", Text: text})
		}
		snap = snap.Apply(patch)
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
	// PromptCacheBreakpoint marks the end of a reusable prefix. It is
	// stamped onto the assembled request, never onto the per-LT cached
	// bytes: the mark moves forward as the conversation grows, and a mark
	// baked into a cached item could never move.
	PromptCacheBreakpoint *promptCacheBreakpoint `json:"prompt_cache_breakpoint,omitempty"`
}

// promptCacheBreakpoint is the GPT-5.6+ explicit cache marker.
//
// Deliberately paired with NO prompt_cache_options.mode="explicit": a bare
// breakpoint is ADDITIVE, leaving OpenAI's automatic breakpoints in place,
// so a bad placement costs nothing. Setting the mode disables the automatic
// ones, and then a single misplaced mark forfeits the whole prefix on every
// turn, at the 1.25x cache-write rate GPT-5.6 introduced.
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

func responseString(snap form.Snapshot, key string) string {
	raw, ok := snap.Get(key)
	if !ok {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return strings.TrimSpace(value)
}

func responseFingerprint(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unset"
	}
	return responsesFingerprintPrefix + "/" + model
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
		return responseRequestOptions{}, fmt.Errorf("copilot responses: system.context_tier must be \"default\" or \"long_context\", got %q", contextTier)
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
		return responseRequestOptions{}, fmt.Errorf("copilot responses: system.reasoning_context must be \"auto\", \"current_turn\", or \"all_turns\", got %q", reasoningContext)
	}
	reasoningSummary, _, err := responseOptionalString(snap, "system.reasoning_summary")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if reasoningSummary != "" && reasoningSummary != "auto" && reasoningSummary != "concise" && reasoningSummary != "detailed" {
		return responseRequestOptions{}, fmt.Errorf("copilot responses: system.reasoning_summary must be \"auto\", \"concise\", or \"detailed\", got %q", reasoningSummary)
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
		return responseRequestOptions{}, fmt.Errorf("copilot responses: system.temperature must be between 0 and 2")
	}
	topP, topPSet, err := responseOptionalFloat(snap, "system.top_p")
	if err != nil {
		return responseRequestOptions{}, err
	}
	if topPSet && (topP <= 0 || topP > 1) {
		return responseRequestOptions{}, fmt.Errorf("copilot responses: system.top_p must be greater than 0 and at most 1")
	}
	if temperatureSet && topPSet {
		return responseRequestOptions{}, fmt.Errorf("copilot responses: set either system.temperature or system.top_p, not both")
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
		return "", false, fmt.Errorf("copilot responses: %s must be a string: %w", key, err)
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
		return false, false, fmt.Errorf("copilot responses: %s must be a boolean: %w", key, err)
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
		return 0, false, fmt.Errorf("copilot responses: %s must be a number: %w", key, err)
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
		return 0, false, fmt.Errorf("copilot responses: %s must be an integer: %w", key, err)
	}
	return value, true, nil
}

type responseObject struct {
	ID     string            `json:"id"`
	Output []json.RawMessage `json:"output"`
	Status string            `json:"status"`
	Usage  responseUsage     `json:"usage"`
	Error  json.RawMessage   `json:"error"`
}

type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type responseStreamEvent struct {
	Type         string             `json:"type"`
	Delta        string             `json:"delta"`
	Text         string             `json:"text"`
	Item         responseOutputItem `json:"item"`
	ItemID       string             `json:"item_id"`
	CallID       string             `json:"call_id"`
	Name         string             `json:"name"`
	Arguments    json.RawMessage    `json:"arguments"`
	Response     responseObject     `json:"response"`
	Error        json.RawMessage    `json:"error"`
	OutputIndex  int                `json:"output_index"`
	ContentIndex int                `json:"content_index"`
}

type responseOutputItem struct {
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	Role      string            `json:"role"`
	Content   []responseContent `json:"content"`
	Summary   []responseContent `json:"summary"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Arguments json.RawMessage   `json:"arguments"`
}

type responseCall struct {
	ID        string
	Name      string
	arguments strings.Builder
	ready     bool
}

func readResponseStream(ctx context.Context, conn *websocket.Conn, bus provider.Bus) (responseObject, error) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	calls := map[string]*responseCall{}
	items := map[string]*responseCall{}
	byIndex := map[int]*responseCall{}
	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(conn, &raw); err != nil {
			if ctx.Err() != nil {
				return responseObject{}, ctx.Err()
			}
			return responseObject{}, fmt.Errorf("copilot responses: receive: %w", err)
		}
		var event responseStreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return responseObject{}, fmt.Errorf("copilot responses: decode event: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				bus.PushDelta(message.Content{Type: message.ContentProse, Text: event.Delta})
			}
		case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if event.Delta != "" {
				bus.PushDelta(message.Content{Type: message.ContentThinking, Text: event.Delta})
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				call := ensureResponseCall(calls, event.Item.CallID, event.Item.Name)
				if event.Item.ID != "" {
					items[event.Item.ID] = call
				}
				byIndex[event.OutputIndex] = call
				if event.Item.Arguments != nil {
					call.arguments.Write(responseArgumentBytes(event.Item.Arguments))
				}
				bus.PushToolInvokeStart(call.ID, call.Name)
			}
		case "response.function_call_arguments.delta":
			call := responseCallFor(calls, items, byIndex, event)
			if call != nil && event.Delta != "" {
				call.arguments.WriteString(event.Delta)
				bus.PushToolInvokeDelta(call.ID, event.Delta)
			}
		case "response.function_call_arguments.done":
			call := responseCallFor(calls, items, byIndex, event)
			if call != nil {
				if len(event.Arguments) > 0 {
					call.arguments.Reset()
					call.arguments.Write(responseArgumentBytes(event.Arguments))
				}
				if err := readyResponseCall(call, bus); err != nil {
					return responseObject{}, err
				}
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				call := ensureResponseCall(calls, event.Item.CallID, event.Item.Name)
				if event.Item.ID != "" {
					items[event.Item.ID] = call
				}
				if event.Item.Arguments != nil {
					call.arguments.Reset()
					call.arguments.Write(responseArgumentBytes(event.Item.Arguments))
				}
				if err := readyResponseCall(call, bus); err != nil {
					return responseObject{}, err
				}
			}
		case "response.completed":
			return event.Response, nil
		case "response.failed", "error":
			if len(event.Error) > 0 && string(event.Error) != "null" {
				return responseObject{}, fmt.Errorf("copilot responses: %s", string(event.Error))
			}
			if len(event.Response.Error) > 0 && string(event.Response.Error) != "null" {
				return responseObject{}, fmt.Errorf("copilot responses: %s", string(event.Response.Error))
			}
			return responseObject{}, fmt.Errorf("copilot responses: %s", event.Type)
		}
	}
}

func ensureResponseCall(calls map[string]*responseCall, id, name string) *responseCall {
	if id == "" {
		id = uuid.NewString()
	}
	if call := calls[id]; call != nil {
		if call.Name == "" {
			call.Name = name
		}
		return call
	}
	call := &responseCall{ID: id, Name: name}
	calls[id] = call
	return call
}

func responseCallFor(calls map[string]*responseCall, items map[string]*responseCall, byIndex map[int]*responseCall, event responseStreamEvent) *responseCall {
	if event.CallID != "" {
		return ensureResponseCall(calls, event.CallID, event.Name)
	}
	if event.ItemID != "" {
		if call := items[event.ItemID]; call != nil {
			return call
		}
	}
	// output_index is the last resort and, on the GitHub Copilot proxy, the
	// ONLY stable handle: it re-encrypts item_id per event, so the id on a
	// delta never equals the one announced at output_item.added and every
	// streamed argument fragment was silently dropped.
	return byIndex[event.OutputIndex]
}

func readyResponseCall(call *responseCall, bus provider.Bus) error {
	if call.ready {
		return nil
	}
	raw := strings.TrimSpace(call.arguments.String())
	arguments, err := decodeResponseArguments(raw)
	if err != nil {
		return fmt.Errorf("copilot responses: function %q arguments: %w", call.Name, err)
	}
	call.ready = true
	bus.PushToolReady(message.Content{
		Type:       message.ContentToolInvoke,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Arguments:  arguments,
	})
	return nil
}

func responseArgumentBytes(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return []byte(encoded)
	}
	return raw
}

func decodeResponseAssistant(response responseObject) (message.Message, error) {
	out := message.Message{
		Role: message.RoleOutput,
		// input_tokens is INCLUSIVE of the cached/written breakdown beside
		// it (the wire proves it: total_tokens == input_tokens +
		// output_tokens). Copying it straight into InputTokens counted every
		// cached token twice in tokens.ContextFromUsage.
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
			return message.Message{}, fmt.Errorf("copilot responses: decode output item: %w", err)
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
			arguments, err := decodeResponseArguments(string(responseArgumentBytes(item.Arguments)))
			if err != nil {
				return message.Message{}, fmt.Errorf("copilot responses: function %q arguments: %w", item.Name, err)
			}
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
	if hasResponseToolInvoke(out.Content) {
		out.StopReason = message.StopToolInvoke
	} else if response.Status == "incomplete" {
		out.StopReason = message.StopLength
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
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "401") || strings.Contains(text, "unauthorized")
}

// toolImageCaption names the call an image came from. The Responses shape
// separates a tool's image from its function_call_output by a message
// boundary, so without a caption a model facing several tool images in one
// turn has nothing to bind them to.
func toolImageCaption(c message.Content) string {
	if c.ToolName != "" {
		return fmt.Sprintf("[image output of tool %s (call %s)]", c.ToolName, c.ToolCallID)
	}
	return fmt.Sprintf("[image output of call %s]", c.ToolCallID)
}

// markPromptCacheBreakpoint stamps one explicit breakpoint at the end of the
// last completed exchange, and reports how many it placed.
//
// Placement is deliberately BEHIND the newest turn. GPT-5.6 already carries
// an implicit breakpoint at the latest user/tool message, and: unlike
// earlier models: it does NOT fall back to the longest matching unmarked
// prefix when that fails. So a changed tail returns cached_tokens=0 even
// though thousands of leading tokens are identical. A mark on the last
// assistant item is a boundary that existed, byte for byte, on the previous
// turn, which gives the miss somewhere to land.
//
// The first turn of an aria has no assistant item and gets no mark: there is
// nothing behind it that was ever cached, and the implicit breakpoint
// already covers what there is.
// It returns a COPY. inputFor hands back projection.State, the live slice the
// provider retains between turns, so stamping in place would bake this
// turn's marker into the per-LT cache permanently: it could never move
// forward, the next turn would add a second one, and the bytes of an item
// already inside a cached prefix would have changed underneath it.
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

// logPromptCacheEconomics records what the cache actually did.
//
// This route speaks websocket, so wirelog: which wraps an http
// RoundTripper: cannot see it and FIGARO_WIRE_DIR yields nothing here. The
// usage numbers are the only instrument available, and on GPT-5.6 they are
// the ones that matter: writes are billed at 1.25x the uncached rate, so a
// breakpoint that re-writes every turn costs more than not caching at all.
// Reads-per-write is the number that tells you which you have.
func logPromptCacheEconomics(usage responseUsage, breakpoints int) {
	reads := usage.InputTokensDetails.CachedTokens
	writes := usage.InputTokensDetails.CacheWriteTokens
	if reads == 0 && writes == 0 {
		return
	}
	slog.Debug("copilot responses prompt cache",
		"cache_read_tokens", reads,
		"cache_write_tokens", writes,
		"uncached_input_tokens", usage.InputTokens-reads-writes,
		"explicit_breakpoints", breakpoints)
}
