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

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

const responsesFingerprint = "copilot-responses/v1"

type responseTokenSource interface {
	Resolve() (string, error)
	Invalidate(string) error
}

type responseDialer func(context.Context, string, http.Header) (*websocket.Conn, error)

type responsesProvider struct {
	tokenSrc  responseTokenSource
	cacheOpen func(string) (store.Log[[]json.RawMessage], error)

	mu        sync.Mutex
	model     string
	maxTokens int
	templates *template.Template
	machineID string
	caches    map[string]store.Log[[]json.RawMessage]
	sessions  map[string]string

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
		caches:    map[string]store.Log[[]json.RawMessage]{},
		sessions:  map[string]string{},
		baseURL:   func(token string) string { return baseURLFromToken(token, enterpriseDomain) },
		dial:      dialResponses,
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

func (p *responsesProvider) Fingerprint() string { return responsesFingerprint }

func (p *responsesProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	token, err := p.tokenSrc.Resolve()
	if err != nil {
		return fmt.Errorf("copilot responses: resolve token: %w", err)
	}

	err = p.sendWithToken(ctx, token, in, bus)
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
	return p.sendWithToken(ctx, token, in, bus)
}

func (p *responsesProvider) sendWithToken(ctx context.Context, token string, in provider.SendInput, bus provider.Bus) error {
	input, err := p.inputFor(in)
	if err != nil {
		return err
	}
	if len(input) == 0 {
		return fmt.Errorf("copilot responses: empty context")
	}

	model, maxTokens, machineID := p.settings(in.Snapshot)
	if model == "" {
		return fmt.Errorf("copilot responses: model is required")
	}
	if in.MaxTokens > 0 {
		maxTokens = in.MaxTokens
	}
	taskID := uuid.NewString()
	sessionID := p.sessionIDFor(in.AriaID)
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
		ParallelToolCalls: true,
		Store:             false,
		Tools:             responseTools(in.Tools),
	}
	if maxTokens > 0 {
		request.MaxOutputTokens = maxTokens
	}
	if effort := responseString(in.Snapshot, "system.thinking_effort"); effort != "" {
		request.Reasoning = &responseReasoning{Effort: effort}
	}
	if verbosity := responseString(in.Snapshot, "system.verbosity"); verbosity != "" {
		request.Text = &responseText{Verbosity: verbosity}
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
	if len(assistant.Content) == 0 && len(response.Output) == 0 {
		return nil
	}
	assistant.Timestamp = time.Now().UnixMilli()
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: assistant})
	if err != nil {
		return fmt.Errorf("copilot responses: append assistant: %w", err)
	}
	assistant.LogicalTime = entry.LT
	bus.PushMessageEnd(string(assistant.StopReason))
	bus.PushFigaro(assistant)

	if cache := p.cacheFor(in.AriaID); cache != nil && len(response.Output) > 0 {
		if _, err := cache.Append(store.Entry[[]json.RawMessage]{
			FigaroLT:    entry.LT,
			Payload:     response.Output,
			Fingerprint: p.Fingerprint(),
		}); err != nil {
			slog.Error("copilot responses cache assistant", "aria", in.AriaID, "err", err)
		}
	}
	return nil
}

func (p *responsesProvider) settings(snap chalkboard.Snapshot) (string, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	model := p.model
	if configured := responseString(snap, "system.model"); configured != "" {
		model = configured
	}
	maxTokens := p.maxTokens
	return model, maxTokens, p.machineID
}

func (p *responsesProvider) sessionIDFor(aria string) string {
	if aria == "" {
		return uuid.NewString()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if sessionID := p.sessions[aria]; sessionID != "" {
		return sessionID
	}
	sessionID := uuid.NewString()
	p.sessions[aria] = sessionID
	return sessionID
}

func (p *responsesProvider) cacheFor(aria string) store.Log[[]json.RawMessage] {
	if aria == "" || p.cacheOpen == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cache, ok := p.caches[aria]; ok {
		p.invalidateCache(cache)
		return cache
	}
	cache, err := p.cacheOpen(aria)
	if err != nil {
		return nil
	}
	p.invalidateCache(cache)
	p.caches[aria] = cache
	return cache
}

func (p *responsesProvider) invalidateCache(cache store.Log[[]json.RawMessage]) {
	for _, entry := range cache.Read() {
		if entry.Fingerprint != "" && entry.Fingerprint != p.Fingerprint() {
			_ = cache.Clear()
			break
		}
	}
}

func (p *responsesProvider) inputFor(in provider.SendInput) ([]json.RawMessage, error) {
	cache := p.cacheFor(in.AriaID)
	var input []json.RawMessage
	snap := chalkboard.Snapshot{}

	for _, entry := range in.FigLog.Read() {
		msg := entry.Payload
		if msg.Role == message.RoleGenesis {
			continue
		}
		patches := msg.Patches
		if in.Chalkboard != nil {
			patches = in.Chalkboard.PatchesAt(entry.LT)
		}

		var encoded []json.RawMessage
		if cache != nil {
			if cached, ok := cache.Lookup(entry.LT); ok && len(cached.Payload) > 0 {
				encoded = cached.Payload
			}
		}
		if encoded == nil {
			var err error
			encoded, err = encodeResponseMessage(msg, patches, snap, p.templatesForEncoding())
			if err != nil {
				return nil, fmt.Errorf("copilot responses: encode message %d: %w", entry.LT, err)
			}
			if cache != nil && len(encoded) > 0 {
				if _, err := cache.Append(store.Entry[[]json.RawMessage]{
					FigaroLT:    entry.LT,
					Payload:     encoded,
					Fingerprint: p.Fingerprint(),
				}); err != nil {
					slog.Error("copilot responses cache message", "aria", in.AriaID, "lt", entry.LT, "err", err)
				}
			}
		}
		input = append(input, encoded...)
		for _, patch := range patches {
			snap = snap.Apply(patch)
		}
	}
	return input, nil
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
	Text              *responseText      `json:"text,omitempty"`
	Tools             []responseTool     `json:"tools,omitempty"`
}

type responseReasoning struct {
	Effort string `json:"effort"`
}

type responseText struct {
	Verbosity string `json:"verbosity"`
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
	snap chalkboard.Snapshot,
	templates *template.Template,
) ([]json.RawMessage, error) {
	var beforeMessage []json.RawMessage
	var afterMessage []json.RawMessage
	var userContent []responseContent
	var assistantContent []responseContent

	for _, content := range msg.Content {
		switch msg.Role {
		case message.RoleUser:
			switch content.Type {
			case message.ContentProse:
				if content.Text != "" {
					userContent = append(userContent, responseContent{Type: "input_text", Text: content.Text})
				}
			case message.ContentImage:
				if content.Data != "" {
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
		case message.RoleAssistant:
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
	if len(assistantContent) > 0 && msg.Role == message.RoleAssistant {
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
	snap chalkboard.Snapshot,
	templates *template.Template,
) ([]string, error) {
	if templates == nil {
		return nil, nil
	}
	rendered, err := chalkboard.Render(patch, snap, templates)
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

func responseInstructions(snap chalkboard.Snapshot) string {
	raw, ok := snap["system.credo"]
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

func responseString(snap chalkboard.Snapshot, key string) string {
	raw, ok := snap[key]
	if !ok {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return strings.TrimSpace(value)
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
				if event.Item.Arguments != nil {
					call.arguments.Write(responseArgumentBytes(event.Item.Arguments))
				}
				bus.PushToolInvokeStart(call.ID, call.Name)
			}
		case "response.function_call_arguments.delta":
			call := responseCallFor(calls, items, event)
			if call != nil && event.Delta != "" {
				call.arguments.WriteString(event.Delta)
				bus.PushToolInvokeDelta(call.ID, event.Delta)
			}
		case "response.function_call_arguments.done":
			call := responseCallFor(calls, items, event)
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

func responseCallFor(calls map[string]*responseCall, items map[string]*responseCall, event responseStreamEvent) *responseCall {
	if event.CallID != "" {
		return ensureResponseCall(calls, event.CallID, event.Name)
	}
	if event.ItemID != "" {
		return items[event.ItemID]
	}
	return nil
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
		Role: message.RoleAssistant,
		Usage: &message.Usage{
			InputTokens:      response.Usage.InputTokens,
			OutputTokens:     response.Usage.OutputTokens,
			CacheReadTokens:  response.Usage.InputTokensDetails.CachedTokens,
			CacheWriteTokens: response.Usage.InputTokensDetails.CacheWriteTokens,
		},
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
