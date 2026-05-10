// Package anthropic implements the figaro Provider for the Anthropic Messages API.
//
// Send drives one turn end-to-end: catches up the translator from the
// figStream, POSTs, streams SSE chunks (decoded inline so figaro IR
// deltas hit the bus as they arrive), and on EOF appends the
// assembled assistant message to figStream and the translator cache.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
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

	// CacheOpen opens the on-disk per-aria translation cache. Nil
	// disables caching (ephemeral arias). The provider owns the
	// returned stream's lifetime — opens lazily on first Send for
	// each aria and keeps it open for the agent's life.
	CacheOpen func(aria string) (store.Stream[[]json.RawMessage], error)
	caches    map[string]store.Stream[[]json.RawMessage] // guarded by mu
}

// New constructs an Anthropic provider. resolver supplies the API
// token on each request; cacheOpen is the per-aria cache file opener,
// nil = ephemeral (no caching). Credential strategy (OAuth vs static
// key) is the caller's choice and is hidden behind resolver.
func New(cfg config.AnthropicProvider, resolver auth.TokenResolver, cacheOpen func(aria string) (store.Stream[[]json.RawMessage], error)) (*Anthropic, error) {
	if resolver == nil {
		return nil, fmt.Errorf("anthropic: nil token resolver")
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
		CacheOpen:        cacheOpen,
		caches:           map[string]store.Stream[[]json.RawMessage]{},
	}, nil
}

// cacheFor returns the per-aria cache, opening it lazily on first
// use. Stale entries (Fingerprint mismatch) get cleared at open. nil
// when no cache is configured or the open failed (provider runs
// without caching for that aria).
func (a *Anthropic) cacheFor(aria string) store.Stream[[]json.RawMessage] {
	if aria == "" || a.CacheOpen == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.caches[aria]; ok {
		return s
	}
	s, err := a.CacheOpen(aria)
	if err != nil {
		slog.Warn("anthropic cache open", "aria", aria, "err", err)
		return nil
	}
	a.invalidateIfStale(s)
	a.caches[aria] = s
	return s
}

// invalidateIfStale clears the cache when stored Fingerprint doesn't
// match the current encoder config.
func (a *Anthropic) invalidateIfStale(s store.Stream[[]json.RawMessage]) {
	want := a.Fingerprint()
	for _, e := range s.Read() {
		if e.Fingerprint == "" || e.Fingerprint == want {
			continue
		}
		_ = s.Clear()
		slog.Info("anthropic cleared stale cache", "stored", e.Fingerprint, "current", want)
		return
	}
}

// setAuthHeaders applies the auth + protocol headers to a request.
// Called both for the initial send and the on-401 retry. The shape
// (Bearer + claude-code beta vs x-api-key) is decided per token by
// the sk-ant-oat prefix.
func (a *Anthropic) setAuthHeaders(req *http.Request, apiKey string, betas string) {
	req.Header.Set("anthropic-version", apiVersion)
	if isOAuthToken(apiKey) {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("anthropic-beta", betas)
		req.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		req.Header.Set("x-app", "cli")
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	} else {
		req.Header.Set("x-api-key", apiKey)
	}
}

const (
	betaModels   = "claude-code-20250219,oauth-2025-04-20"
	betaMessages = "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,prompt-caching-2024-07-31"
)

// doWithAuthRetry executes build(apiKey)→Do once; on 401 it
// invalidates the token, resolves a fresh one, and retries exactly
// once. Returns the final response (caller closes body) plus the
// token that was actually accepted.
func (a *Anthropic) doWithAuthRetry(ctx context.Context, build func(apiKey string) (*http.Request, error)) (*http.Response, string, error) {
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, "", fmt.Errorf("resolve token: %w", err)
	}
	req, err := build(apiKey)
	if err != nil {
		return nil, apiKey, err
	}
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, apiKey, fmt.Errorf("http: %w", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, apiKey, nil
	}
	// 401: drain & close, then retry once with a fresh token.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	a.auth.Invalidate(apiKey)
	newKey, rerr := a.auth.Resolve()
	if rerr != nil {
		return nil, apiKey, fmt.Errorf("resolve after 401: %w", rerr)
	}
	if newKey == apiKey {
		return nil, apiKey, fmt.Errorf("anthropic 401: token unchanged after invalidate")
	}
	req2, err := build(newKey)
	if err != nil {
		return nil, newKey, err
	}
	resp2, err := a.HTTPClient.Do(req2)
	if err != nil {
		return nil, newKey, fmt.Errorf("http retry: %w", err)
	}
	return resp2, newKey, nil
}

func (a *Anthropic) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	resp, _, err := a.doWithAuthRetry(ctx, func(apiKey string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", apiBaseURL+"/models?limit=100", nil)
		if err != nil {
			return nil, err
		}
		a.setAuthHeaders(req, apiKey, betaModels)
		return req, nil
	})
	if err != nil {
		return nil, err
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
func (a *Anthropic) encode(msg message.Message, prevSnapshot chalkboard.Snapshot) ([]json.RawMessage, error) {
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
		slog.Warn("anthropic: reminder_renderer=tool not supported inline; using tag")
	}
	var out []nativeBlock
	for _, p := range patches {
		rendered, err := chalkboard.Render(p, *prevSnap, a.Templates)
		if err != nil {
			slog.Warn("anthropic: render patch", "err", err)
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
		var entries []outfit.SkillCatalogEntry
		if json.Unmarshal(raw, &entries) == nil && len(entries) > 0 {
			body := outfit.FormatSkillCatalog(entries)
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
	return a.projectMessagesWithLTs(perMessage, nil, snapshot, tools, maxTokens, oauth, model)
}

// projectMessagesWithLTs is the actual assembler. lts[i] is the
// figStream logical time corresponding to perMessage[i]; pass nil to
// skip per-message tag application.
func (a *Anthropic) projectMessagesWithLTs(perMessage [][]json.RawMessage, lts []uint64, snapshot chalkboard.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, error) {
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
	var msgLTs []uint64
	for i, entry := range perMessage {
		var lt uint64
		if i < len(lts) {
			lt = lts[i]
		}
		for _, raw := range entry {
			if len(raw) == 0 {
				continue
			}
			var nm nativeMessage
			if err := json.Unmarshal(raw, &nm); err != nil {
				return nativeRequest{}, fmt.Errorf("unmarshal cached message: %w", err)
			}
			req.Messages = append(req.Messages, nm)
			msgLTs = append(msgLTs, lt)
		}
	}

	if cacheSetting := snapshot.Lookup("system.cache_control"); cacheSetting != nil {
		markCacheBreakpoints(&req, *cacheSetting)
	}
	applyMessageTags(&req, msgLTs, snapshot)
	return req, nil
}

// applyMessageTags reads `system.tags` from the snapshot and applies
// per-message overrides. The shape is:
//
//	{"<lt>": {"cache_control": "ephemeral"}}
//
// For each lt that maps to a wire message, the named cache_control
// type is attached to that message's last content block. Unknown lts
// are silently ignored — the tag will activate when (or if) that
// message ever appears in the wire stream.
func applyMessageTags(req *nativeRequest, msgLTs []uint64, snapshot chalkboard.Snapshot) {
	raw, ok := snapshot["system.tags"]
	if !ok || len(raw) == 0 {
		return
	}
	var tags map[string]struct {
		CacheControl string `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return
	}
	if len(tags) == 0 {
		return
	}
	// Build lt → last wire-index map (last wins for multi-block messages).
	lastIdx := make(map[uint64]int, len(msgLTs))
	for i, lt := range msgLTs {
		if lt == 0 {
			continue
		}
		lastIdx[lt] = i
	}
	for key, tag := range tags {
		if tag.CacheControl == "" {
			continue
		}
		lt, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			continue
		}
		idx, ok := lastIdx[lt]
		if !ok {
			continue
		}
		m := &req.Messages[idx]
		if k := len(m.Content); k > 0 {
			m.Content[k-1].CacheControl = &cacheControl{Type: tag.CacheControl}
		}
	}
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

// --- Send: pipeline SSE → assistant message ---

// Send drives one turn end-to-end: catches up the per-aria translator
// (cache) from the figStream, POSTs, streams SSE chunks (decoding
// inline so figaro IR deltas hit the bus as they arrive), and on EOF
// lands the assembled assistant message in figStream + cache, then
// announces via bus.PushFigaro. The cache stores one entry per
// message.
func (a *Anthropic) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	cache := a.cacheFor(in.AriaID)
	perMessage, lts := a.catchUp(in.FigStream, cache)
	if len(perMessage) == 0 {
		return fmt.Errorf("empty context")
	}

	apiKey, err := a.auth.Resolve()
	if err != nil {
		return fmt.Errorf("resolve token: %w", err)
	}
	model := a.resolveModel(in.Snapshot)

	req, err := a.projectMessagesWithLTs(perMessage, lts, in.Snapshot, in.Tools, in.MaxTokens, isOAuthToken(apiKey), model)
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, _, err := a.doWithAuthRetry(ctx, func(token string) (*http.Request, error) {
		httpReq, herr := http.NewRequestWithContext(ctx, "POST", apiMessagesURL, bytes.NewReader(body))
		if herr != nil {
			return nil, fmt.Errorf("create request: %w", herr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		a.setAuthHeaders(httpReq, token, betaMessages)
		return httpReq, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}

	nm, err := a.drainSSE(ctx, resp.Body, model, bus)
	if err != nil {
		// Broken stream (interrupt, EOF mid-message, scanner error,
		// timeout, error event). Drop everything drainSSE buffered —
		// no append, no PushFigaro, no cache write. The next turn
		// starts from a well-formed figStream.
		return err
	}
	if len(nm.Content) == 0 {
		return nil
	}

	// Land the assistant message: figStream → push figaro → cache.
	msg := decodeNativeMessage(nm)
	entry, err := in.FigStream.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return fmt.Errorf("append assistant: %w", err)
	}
	msg.LogicalTime = entry.LT
	bus.PushFigaro(msg)

	if cache != nil {
		// Re-encode strips inbound-only fields the API rejects (stop_reason
		// etc.). Cached bytes go in input-ready.
		if encoded, err := a.encode(msg, chalkboard.Snapshot{}); err == nil {
			_, _ = cache.Append(store.Entry[[]json.RawMessage]{
				FigaroLT:    entry.LT,
				Payload:     encoded,
				Fingerprint: a.Fingerprint(),
			})
		} else {
			slog.Error("anthropic re-encode assistant", "err", err)
		}
	}
	return nil
}

func (a *Anthropic) resolveModel(snap chalkboard.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return *v
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Model
}

// catchUp encodes any figStream entries that don't yet have a cache
// hit and returns the per-message wire bytes for the request body.
// Cache write is best-effort — encoding always produces the bytes
// regardless.
func (a *Anthropic) catchUp(figStream store.Stream[message.Message], cache store.Stream[[]json.RawMessage]) ([][]json.RawMessage, []uint64) {
	fp := a.Fingerprint()
	snap := chalkboard.Snapshot{}
	var perMessage [][]json.RawMessage
	var lts []uint64
	for _, e := range figStream.Read() {
		msg := e.Payload
		msg.LogicalTime = e.LT
		var bytes []json.RawMessage
		if cache != nil {
			if existing, ok := cache.Lookup(msg.LogicalTime); ok && len(existing.Payload) > 0 {
				bytes = existing.Payload
			}
		}
		if bytes == nil {
			encoded, err := a.encode(msg, snap)
			if err != nil {
				slog.Error("anthropic encode", "flt", msg.LogicalTime, "err", err)
			} else {
				bytes = encoded
				if cache != nil {
					_, _ = cache.Append(store.Entry[[]json.RawMessage]{
						FigaroLT: msg.LogicalTime, Payload: bytes, Fingerprint: fp,
					})
				}
			}
		}
		if len(bytes) > 0 {
			perMessage = append(perMessage, bytes)
			lts = append(lts, msg.LogicalTime)
		}
		for _, p := range msg.Patches {
			snap = snap.Apply(p)
		}
	}
	return perMessage, lts
}

const sseReadTimeout = 5 * time.Minute

// drainSSE is the SSE pipeline: read events one at a time, fold each
// content_block_delta into the in-memory accumulator and emit a
// figaro IR delta on the bus, return the final nativeMessage at
// message_stop. No external state is touched and nothing gets
// persisted — the live deltas are purely ephemeral.
//
// Returns a nil error only on a clean message_stop. Any other
// termination (context cancel / interrupt, scanner error, EOF
// before message_stop, read timeout, SSE error event) returns a
// non-nil error and the caller MUST drop the partial nm — it is
// not safe to persist (tool_use blocks may have unclosed inputs).
func (a *Anthropic) drainSSE(ctx context.Context, body io.ReadCloser, model string, bus provider.Bus) (nativeMessage, error) {
	nm := nativeMessage{Role: "assistant", Model: model}
	var (
		usage      nativeUsage
		stopReason string
	)

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
					slog.Warn("anthropic sse body read", "err", err)
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

	finalizeClean := func() (nativeMessage, error) {
		if stopReason == "" {
			stopReason = "end_turn"
		}
		nm.StopReason = stopReason
		nm.Usage = &usage
		return nm, nil
	}

	var (
		eventType string
		lines     int
	)
	scanNext()
	for {
		var line string
		select {
		case <-ctx.Done():
			slog.Debug("anthropic sse interrupted", "lines", lines, "err", ctx.Err())
			return nm, ctx.Err()
		case line = <-scanCh:
			lines++
			scanNext()
		case err := <-scanErr:
			if err != nil {
				slog.Warn("anthropic sse scanner", "lines", lines, "err", err)
				return nm, fmt.Errorf("sse scanner: %w", err)
			}
			slog.Warn("anthropic sse stream ended before message_stop", "lines", lines)
			return nm, fmt.Errorf("sse stream ended before message_stop")
		case <-time.After(sseReadTimeout):
			slog.Warn("anthropic sse read timeout", "lines", lines)
			return nm, fmt.Errorf("sse read timeout")
		}
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := []byte(strings.TrimPrefix(line, "data: "))
		a.foldSSEEvent(eventType, data, &nm, &usage, &stopReason, bus)
		if eventType == "message_stop" {
			return finalizeClean()
		}
		if eventType == "error" {
			return nm, fmt.Errorf("sse error event: %s", string(data))
		}
	}
}

// foldSSEEvent updates the accumulator nm + usage + stopReason with
// one decoded SSE event and pushes any figaro IR delta on the bus.
func (a *Anthropic) foldSSEEvent(eventType string, data []byte, nm *nativeMessage, usage *nativeUsage, stopReason *string, bus provider.Bus) {
	switch eventType {
	case "content_block_start":
		var block struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id,omitempty"`
				Name string `json:"name,omitempty"`
			} `json:"content_block"`
		}
		if json.Unmarshal(data, &block) != nil {
			return
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
		if json.Unmarshal(data, &d) != nil || d.Index >= len(nm.Content) {
			return
		}
		b := &nm.Content[d.Index]
		switch d.Delta.Type {
		case "text_delta":
			b.Text += d.Delta.Text
			if d.Delta.Text != "" {
				bus.PushDelta(message.Content{Type: message.ContentText, Text: d.Delta.Text})
			}
		case "thinking_delta":
			b.Thinking += d.Delta.Thinking
			if d.Delta.Thinking != "" {
				bus.PushDelta(message.Content{Type: message.ContentThinking, Text: d.Delta.Thinking})
			}
		case "input_json_delta":
			if s, ok := b.Input.(string); ok {
				b.Input = s + d.Delta.PartialJSON
			} else {
				b.Input = d.Delta.PartialJSON
			}
		}
	case "content_block_stop":
		var stop struct{ Index int }
		if json.Unmarshal(data, &stop) != nil || stop.Index >= len(nm.Content) {
			return
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
		if json.Unmarshal(data, &ms) == nil {
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
		if json.Unmarshal(data, &md) == nil {
			usage.OutputTokens = md.Usage.OutputTokens
			if md.Delta.StopReason != "" {
				*stopReason = md.Delta.StopReason
			}
		}
	case "message_stop":
		// terminator; finalizeClean() runs from drainSSE
	case "error":
		*stopReason = "error"
	}
}
