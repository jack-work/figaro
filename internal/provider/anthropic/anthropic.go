// Package anthropic implements the figaro Provider for the Anthropic Messages API.
//
// Per-message encoding (Encode) is cached in the agent's translator
// stream. Send takes the cached bytes plus the snapshot and assembles
// the request body internally before shipping. SSE deltas are pushed
// raw to the bus; Assemble folds them into the final assistant bytes
// at end-of-turn. Decode reverses native bytes back to IR uniformly
// for both durable per-message entries and live tail delta payloads.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	hush "github.com/jack-work/hush/client"
)

const (
	providerName      = "anthropic"
	apiBaseURL        = "https://api.anthropic.com/v1"
	apiMessagesURL    = apiBaseURL + "/messages"
	apiVersion        = "2023-06-01"
	claudeCodeVersion = "2.1.62"
)

type Anthropic struct {
	auth             auth.TokenResolver
	mu               sync.Mutex
	Model            string
	MaxTokens        int
	HTTPClient       *http.Client
	ReminderRenderer string // "tag" (default) or "tool"

	// Templates is the chalkboard body-template set used to render
	// per-message Patches as system-reminder content blocks. Optional;
	// nil means patches are dropped (no reminder content emitted).
	Templates *template.Template
}

func New(cfg config.AnthropicProvider, authPath string, hushClient *hush.Client) (*Anthropic, error) {
	resolver, err := buildResolver(cfg.APIKey, authPath, hushClient)
	if err != nil {
		return nil, err
	}
	rr := cfg.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return &Anthropic{
		auth:             resolver,
		Model:            cfg.Model,
		MaxTokens:        cfg.MaxTokens,
		HTTPClient:       &http.Client{Timeout: 10 * time.Minute},
		ReminderRenderer: rr,
	}, nil
}

func buildResolver(apiKey, authPath string, hushClient *hush.Client) (auth.TokenResolver, error) {
	if apiKey != "" {
		return &auth.StaticKey{Key: apiKey}, nil
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return &auth.StaticKey{Key: key}, nil
	}
	if hushClient == nil {
		return nil, fmt.Errorf(
			"no API key and no hush client available.\n" +
				"  Set api_key in config, ANTHROPIC_API_KEY env, or run: figaro login",
		)
	}
	mgr := auth.NewManager(hushClient, auth.AnthropicOAuth, authPath)
	return &auth.OAuthResolver{Manager: mgr}, nil
}

func (a *Anthropic) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiBaseURL+"/models?limit=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", apiVersion)
	if isOAuthToken(apiKey) {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		req.Header.Set("x-app", "cli")
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	} else {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic models API %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	var models []provider.ModelInfo
	for _, m := range result.Data {
		models = append(models, provider.ModelInfo{
			ID:       m.ID,
			Name:     m.DisplayName,
			Provider: providerName,
		})
	}
	return models, nil
}

func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

func (a *Anthropic) Name() string { return providerName }

// Fingerprint hashes the encoder config. Mismatch invalidates the
// translator cache.
func (a *Anthropic) Fingerprint() string {
	rr := a.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return "anthropic/" + rr + "/v3"
}

func (a *Anthropic) SetModel(model string) {
	a.mu.Lock()
	a.Model = model
	a.mu.Unlock()
}

// Decode reverses native wire bytes to IR. Auto-detects between
// liveDelta (live tail entries from consumeSSE) and nativeMessage
// (durable per-message entries). Live deltas yield a partial Message
// with only the streamed fragment as content; non-text events
// (block start/stop, message_start, etc.) yield no message.
func (a *Anthropic) Decode(payload []json.RawMessage) ([]message.Message, error) {
	out := make([]message.Message, 0, len(payload))
	for i, r := range payload {
		if len(r) == 0 {
			continue
		}
		// Live tail entry? — has an "event" field.
		var ld liveDelta
		if json.Unmarshal(r, &ld) == nil && ld.Event != "" {
			if msg, ok := decodeLiveDelta(ld); ok {
				out = append(out, msg)
			}
			continue
		}
		// Otherwise: a durable nativeMessage.
		var nm nativeMessage
		if err := json.Unmarshal(r, &nm); err != nil {
			return nil, fmt.Errorf("anthropic decode [%d]: %w", i, err)
		}
		if nm.Role == "" {
			continue
		}
		out = append(out, decodeNativeMessage(nm))
	}
	return out, nil
}

func decodeLiveDelta(ld liveDelta) (message.Message, bool) {
	if ld.Event != "content_block_delta" {
		return message.Message{}, false
	}
	var d struct {
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			Thinking string `json:"thinking,omitempty"`
		} `json:"delta"`
	}
	if json.Unmarshal(ld.Data, &d) != nil {
		return message.Message{}, false
	}
	switch d.Delta.Type {
	case "text_delta":
		if d.Delta.Text == "" {
			return message.Message{}, false
		}
		return message.Message{
			Role:    message.RoleAssistant,
			Content: []message.Content{{Type: message.ContentText, Text: d.Delta.Text}},
		}, true
	case "thinking_delta":
		if d.Delta.Thinking == "" {
			return message.Message{}, false
		}
		return message.Message{
			Role:    message.RoleAssistant,
			Content: []message.Content{{Type: message.ContentThinking, Text: d.Delta.Thinking}},
		}, true
	}
	return message.Message{}, false
}

func decodeNativeMessage(nm nativeMessage) message.Message {
	m := message.Message{
		Role:     message.Role(nm.Role),
		Provider: providerName,
		Model:    nm.Model,
	}
	for _, b := range nm.Content {
		switch b.Type {
		case "text":
			m.Content = append(m.Content, message.Content{Type: message.ContentText, Text: b.Text})
		case "thinking":
			m.Content = append(m.Content, message.Content{Type: message.ContentThinking, Text: b.Thinking})
		case "tool_use":
			args, _ := b.Input.(map[string]interface{})
			m.Content = append(m.Content, message.Content{
				Type: message.ContentToolCall, ToolCallID: b.ID, ToolName: b.Name, Arguments: args,
			})
		case "tool_result":
			var text string
			switch v := b.Content.(type) {
			case string:
				text = v
			case []interface{}:
				for _, item := range v {
					if mm, ok := item.(map[string]interface{}); ok {
						if t, _ := mm["text"].(string); t != "" {
							text += t
						}
					}
				}
			}
			m.Content = append(m.Content, message.Content{
				Type: message.ContentToolResult, ToolCallID: b.ToolUseID, Text: text, IsError: b.IsError,
			})
		}
	}
	switch nm.StopReason {
	case "end_turn", "stop":
		m.StopReason = message.StopEnd
	case "max_tokens", "length":
		m.StopReason = message.StopLength
	case "tool_use":
		m.StopReason = message.StopToolUse
	}
	if nm.Usage != nil {
		m.Usage = &message.Usage{
			InputTokens:      nm.Usage.InputTokens,
			OutputTokens:     nm.Usage.OutputTokens,
			CacheReadTokens:  nm.Usage.CacheRead,
			CacheWriteTokens: nm.Usage.CacheCreate,
		}
	}
	return m
}

func log(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "anthropic: "+format+"\n", args...)
}

// --- Native types ---

type nativeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    []systemBlock   `json:"system,omitempty"`
	Messages  []nativeMessage `json:"messages"`
	Tools     []nativeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type nativeMessage struct {
	Role       string        `json:"role"`
	Content    []nativeBlock `json:"content"`
	StopReason string        `json:"stop_reason,omitempty"`
	Model      string        `json:"model,omitempty"`
	Usage      *nativeUsage  `json:"usage,omitempty"`
}

type nativeUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	CacheRead    int `json:"cache_read_input_tokens,omitempty"`
	CacheCreate  int `json:"cache_creation_input_tokens,omitempty"`
}

type nativeBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	Thinking     string        `json:"thinking,omitempty"`
	Signature    string        `json:"signature,omitempty"`
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Input        interface{}   `json:"input,omitempty"`
	ToolUseID    string        `json:"tool_use_id,omitempty"`
	IsError      bool          `json:"is_error,omitempty"`
	Content      interface{}   `json:"content,omitempty"`
	Source       interface{}   `json:"source,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type nativeTool struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	InputSchema  interface{}   `json:"input_schema"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// --- Encoding ---

// Encode projects one IR message to native wire bytes. Returns nil
// for state-only tics.
func (a *Anthropic) Encode(msg message.Message, prevSnapshot chalkboard.Snapshot) ([]json.RawMessage, error) {
	snap := prevSnapshot
	nm, ok := a.renderMessage(msg, &snap)
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(nm)
	if err != nil {
		return nil, fmt.Errorf("marshal nativeMessage: %w", err)
	}
	return []json.RawMessage{raw}, nil
}

// renderMessage produces the wire shape and advances prevSnap so
// callers chaining renders stay in step.
func (a *Anthropic) renderMessage(msg message.Message, prevSnap *chalkboard.Snapshot) (nativeMessage, bool) {
	switch msg.Role {
	case message.RoleUser:
		var blocks []nativeBlock
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentText:
				blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
			case message.ContentImage:
				blocks = append(blocks, nativeBlock{
					Type: "image",
					Source: map[string]any{
						"type": "base64", "media_type": c.MimeType, "data": c.Data,
					},
				})
			case message.ContentToolResult:
				text := c.Text
				if text == "" {
					text = "(empty)"
				}
				blocks = append(blocks, nativeBlock{
					Type: "tool_result", ToolUseID: c.ToolCallID,
					IsError: c.IsError,
					Content: []nativeBlock{{Type: "text", Text: text}},
				})
			}
		}
		blocks = append(blocks, a.renderPatchBlocks(msg.Patches, prevSnap)...)
		if len(blocks) == 0 {
			return nativeMessage{}, false
		}
		return nativeMessage{Role: "user", Content: blocks}, true

	case message.RoleAssistant:
		var blocks []nativeBlock
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentText:
				blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
			case message.ContentThinking:
				blocks = append(blocks, nativeBlock{Type: "thinking", Thinking: c.Text})
			case message.ContentToolCall:
				blocks = append(blocks, nativeBlock{
					Type: "tool_use", ID: c.ToolCallID, Name: c.ToolName, Input: c.Arguments,
				})
			}
		}
		if len(blocks) == 0 {
			return nativeMessage{}, false
		}
		return nativeMessage{Role: "assistant", Content: blocks}, true
	}
	return nativeMessage{}, false
}

func (a *Anthropic) renderPatchBlocks(patches []message.Patch, prevSnap *chalkboard.Snapshot) []nativeBlock {
	if len(patches) == 0 || a.Templates == nil {
		for _, p := range patches {
			*prevSnap = prevSnap.Apply(p)
		}
		return nil
	}
	if a.ReminderRenderer == "tool" {
		log("warning: reminder_renderer=tool not supported inline; using tag")
	}
	var out []nativeBlock
	for _, p := range patches {
		rendered, err := chalkboard.Render(p, *prevSnap, a.Templates)
		if err != nil {
			log("warning: render patch: %v", err)
		} else {
			for _, r := range rendered {
				text := fmt.Sprintf("<system-reminder name=\"%s\">\n%s\n</system-reminder>",
					escapeAttr(r.Key), r.Body)
				out = append(out, nativeBlock{Type: "text", Text: text})
			}
		}
		*prevSnap = prevSnap.Apply(p)
	}
	return out
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

func projectTools(tools []provider.Tool) []nativeTool {
	result := make([]nativeTool, len(tools))
	for i, t := range tools {
		result[i] = nativeTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters}
	}
	return result
}

// --- System blocks + request assembly ---

// systemBlocks builds the system prefix in order: OAuth preamble
// (oauth only), credo (system.prompt), skill catalog
// (system.skills). No cache_control.
func systemBlocks(snapshot chalkboard.Snapshot, oauth bool) []systemBlock {
	var out []systemBlock
	var systemText string
	if raw, ok := snapshot["system.prompt"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			systemText = s
		}
	}
	if oauth {
		out = append(out, systemBlock{Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			out = append(out, systemBlock{Type: "text", Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code — follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		out = append(out, systemBlock{Type: "text", Text: systemText})
	}
	if raw, ok := snapshot["system.skills"]; ok && len(raw) > 0 {
		var entries []credo.SkillCatalogEntry
		if json.Unmarshal(raw, &entries) == nil && len(entries) > 0 {
			body := credo.FormatSkillCatalog(entries)
			if body != "" {
				out = append(out, systemBlock{Type: "text", Text: body})
			}
		}
	}
	return out
}

// projectMessagesWithModel is the pure assembler: cached per-message
// bytes + snapshot + tools → nativeRequest with cache markers. No
// auth resolution; used by Send and tests.
func (a *Anthropic) projectMessagesWithModel(perMessage [][]json.RawMessage, snapshot chalkboard.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, error) {
	if maxTokens == 0 {
		maxTokens = a.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}
	req := nativeRequest{
		Model: model, MaxTokens: maxTokens, Stream: true,
		System: systemBlocks(snapshot, oauth),
		// agent:
		// TODO: tools should also just be put onto the chalkboard and extracted with string, as an ordered list.  we should optimize the chalkboard around a byte for byte exactness when roundtrip, so we don't have to worry about tools messing up the cache.
		Tools: projectTools(tools),
	}
	for _, entry := range perMessage {
		for _, raw := range entry {
			if len(raw) == 0 {
				continue
			}
			var nm nativeMessage
			if err := json.Unmarshal(raw, &nm); err != nil {
				return nativeRequest{}, fmt.Errorf("unmarshal cached message: %w", err)
			}
			req.Messages = append(req.Messages, nm)
		}
	}

	if cacheSetting := snapshot.Lookup("system.cache_control"); cacheSetting != nil {
		markCacheBreakpoints(&req, *cacheSetting)
	}
	return req, nil
}

// markCacheBreakpoints attaches cache_control:(input) to the last
// block of each cacheable region: system prefix, tool list, and the
// second-to-last message's last content block (the prior endTurn
// leaf). The OAuth + claude-code-20250219 beta path silently
// ignores client-controlled cache_control.
func markCacheBreakpoints(req *nativeRequest, setting string) {
	if n := len(req.System); n > 0 {
		req.System[n-1].CacheControl = &cacheControl{Type: setting}
	}
	if n := len(req.Tools); n > 0 {
		req.Tools[n-1].CacheControl = &cacheControl{Type: setting}
	}
	if n := len(req.Messages); n >= 2 {
		m := &req.Messages[n-2]
		if k := len(m.Content); k > 0 {
			m.Content[k-1].CacheControl = &cacheControl{Type: setting}
		}
	}
}

// --- Send: assemble → HTTP → SSE ---

// Send assembles the request body from in.PerMessage + in.Snapshot,
// POSTs it, and pushes raw native deltas to the bus until the
// stream closes. Model: snapshot["system.model"] wins, a.Model is
// the fallback.
func (a *Anthropic) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return fmt.Errorf("resolve token: %w", err)
	}

	var model *string
	if model = in.Snapshot.Lookup("system.model"); model == nil {
		a.mu.Lock()
		model = &a.Model
		a.mu.Unlock()
	}

	req, err := a.projectMessagesWithModel(in.PerMessage, in.Snapshot, in.Tools, in.MaxTokens, isOAuthToken(apiKey), *model)
	if err != nil {
		return err
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiMessagesURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)
	if isOAuthToken(apiKey) {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,prompt-caching-2024-07-31")
		httpReq.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		httpReq.Header.Set("x-app", "cli")
		httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	} else {
		httpReq.Header.Set("x-api-key", apiKey)
	}

	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}
	consumeSSE(resp.Body, bus)
	return nil
}

const sseReadTimeout = 5 * time.Minute

// liveDelta is one entry on the translator's live tail. Event is
// the SSE event name; Data is the SSE data payload. Model is set
// only on the synthetic "_start" entry so Assemble can stamp the
// model the API actually used (real SSE doesn't echo it back).
type liveDelta struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	Model string          `json:"model,omitempty"`
}

// consumeSSE forwards every SSE event to the bus and exits when the
// stream closes. No accumulation — Assemble re-runs the state
// machine at condense time.
func consumeSSE(body io.ReadCloser, bus provider.Bus) {
	defer body.Close()

	push := func(eventType string, data []byte) {
		entry, _ := json.Marshal(liveDelta{Event: eventType, Data: json.RawMessage(data)})
		bus.Push(provider.Event{Payload: []json.RawMessage{entry}})
	}
	startEntry, _ := json.Marshal(liveDelta{Event: "_start"})
	bus.Push(provider.Event{Payload: []json.RawMessage{startEntry}})

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				pw.Write(buf[:n])
			}
			if err != nil {
				if err != io.EOF {
					log("sse: body read error: %v", err)
				}
				return
			}
		}
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	scanCh := make(chan string, 1)
	scanErr := make(chan error, 1)
	scanNext := func() {
		go func() {
			if scanner.Scan() {
				scanCh <- scanner.Text()
			} else {
				scanErr <- scanner.Err()
			}
		}()
	}

	var (
		eventType string
		lines     int
	)
	scanNext()
	for {
		var line string
		select {
		case line = <-scanCh:
			lines++
			scanNext()
		case err := <-scanErr:
			if err != nil {
				log("sse: scanner error after %d lines: %v", lines, err)
			} else {
				log("sse: stream ended after %d lines (EOF)", lines)
			}
			push("_eof", nil)
			return
		case <-time.After(sseReadTimeout):
			log("sse: read timeout after %d lines", lines)
			push("_eof", nil)
			return
		}
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		push(eventType, []byte(data))
		if eventType == "message_stop" || eventType == "error" {
			return
		}
	}
}

// --- Live tail accumulation ---

// Assemble runs the SSE accumulator over the live tail, producing
// the assembled assistant nativeMessage bytes. Returns nil for an
// empty/malformed tail.
func (a *Anthropic) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	var (
		nm         nativeMessage
		usage      nativeUsage
		stopReason string
	)
	nm.Role = "assistant"

	for _, payload := range deltas {
		if len(payload) == 0 {
			continue
		}
		var ld liveDelta
		if err := json.Unmarshal(payload[0], &ld); err != nil {
			continue
		}
		switch ld.Event {
		case "_start":
			if ld.Model != "" {
				nm.Model = ld.Model
			}
		case "_eof":
			if stopReason == "" {
				stopReason = "aborted"
			}
		case "content_block_start":
			var block struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id,omitempty"`
					Name string `json:"name,omitempty"`
				} `json:"content_block"`
			}
			if json.Unmarshal(ld.Data, &block) != nil {
				continue
			}
			for len(nm.Content) <= block.Index {
				nm.Content = append(nm.Content, nativeBlock{})
			}
			switch block.ContentBlock.Type {
			case "text":
				nm.Content[block.Index] = nativeBlock{Type: "text"}
			case "thinking":
				nm.Content[block.Index] = nativeBlock{Type: "thinking"}
			case "tool_use":
				nm.Content[block.Index] = nativeBlock{
					Type: "tool_use", ID: block.ContentBlock.ID, Name: block.ContentBlock.Name,
				}
			}
		case "content_block_delta":
			var d struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text,omitempty"`
					Thinking    string `json:"thinking,omitempty"`
					PartialJSON string `json:"partial_json,omitempty"`
				} `json:"delta"`
			}
			if json.Unmarshal(ld.Data, &d) != nil || d.Index >= len(nm.Content) {
				continue
			}
			b := &nm.Content[d.Index]
			switch d.Delta.Type {
			case "text_delta":
				b.Text += d.Delta.Text
			case "thinking_delta":
				b.Thinking += d.Delta.Thinking
			case "input_json_delta":
				if s, ok := b.Input.(string); ok {
					b.Input = s + d.Delta.PartialJSON
				} else {
					b.Input = d.Delta.PartialJSON
				}
			}
		case "content_block_stop":
			var stop struct{ Index int }
			if json.Unmarshal(ld.Data, &stop) != nil || stop.Index >= len(nm.Content) {
				continue
			}
			b := &nm.Content[stop.Index]
			if b.Type == "tool_use" {
				if s, ok := b.Input.(string); ok && s != "" {
					var args map[string]interface{}
					if json.Unmarshal([]byte(s), &args) == nil {
						b.Input = args
					}
				} else if b.Input == nil {
					b.Input = map[string]interface{}{}
				}
			}
		case "message_start":
			var ms struct {
				Message struct {
					Usage struct {
						InputTokens int `json:"input_tokens"`
						CacheRead   int `json:"cache_read_input_tokens"`
						CacheCreate int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(ld.Data, &ms) == nil {
				usage.InputTokens = ms.Message.Usage.InputTokens
				usage.CacheRead = ms.Message.Usage.CacheRead
				usage.CacheCreate = ms.Message.Usage.CacheCreate
			}
		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(ld.Data, &md) == nil {
				usage.OutputTokens = md.Usage.OutputTokens
				if md.Delta.StopReason != "" {
					stopReason = md.Delta.StopReason
				}
			}
		case "message_stop":
			// terminator; nothing more to fold
		case "error":
			stopReason = "error"
		}
	}

	if len(nm.Content) == 0 {
		return nil, nil
	}
	if stopReason == "" {
		stopReason = "aborted"
	}
	nm.StopReason = stopReason
	nm.Usage = &usage
	raw, err := json.Marshal(nm)
	if err != nil {
		return nil, fmt.Errorf("marshal assembled: %w", err)
	}
	return []json.RawMessage{raw}, nil
}
