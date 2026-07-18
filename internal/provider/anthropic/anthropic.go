// Package anthropic implements the Provider for the Anthropic Messages API.
package anthropic

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
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
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

	// Templates renders Patches as system-reminder blocks. nil = skip.
	Templates *template.Template

	// CacheOpen opens the per-aria translation cache. nil = no caching.
	CacheOpen func(aria string) (store.Log[[]json.RawMessage], error)
	caches    map[string]store.Log[[]json.RawMessage] // guarded by mu
	snapCache map[string]*translationSnapshot
}

type translationSnapshot struct {
	snap       chalkboard.Snapshot
	perMessage [][]json.RawMessage
	lts        []uint64
	nEntries   int
	lastLT     uint64
}

// New constructs an Anthropic provider.
func New(knobs provider.Knobs, resolver auth.TokenResolver, cacheOpen func(aria string) (store.Log[[]json.RawMessage], error)) (*Anthropic, error) {
	if resolver == nil {
		return nil, fmt.Errorf("anthropic: nil token resolver")
	}
	rr := knobs.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return &Anthropic{
		auth:             resolver,
		Model:            knobs.Model,
		MaxTokens:        knobs.MaxTokens,
		HTTPClient:       &http.Client{Timeout: 10 * time.Minute},
		ReminderRenderer: rr,
		CacheOpen:        cacheOpen,
		caches:           map[string]store.Log[[]json.RawMessage]{},
		snapCache:        map[string]*translationSnapshot{},
	}, nil
}

// cacheFor returns the per-aria cache, opening lazily. nil if
// unconfigured or open failed.
func (a *Anthropic) cacheFor(aria string) store.Log[[]json.RawMessage] {
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

// invalidateIfStale clears the cache on fingerprint mismatch.
func (a *Anthropic) invalidateIfStale(s store.Log[[]json.RawMessage]) {
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

// setAuthHeaders applies auth + protocol headers.
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

// maxTransientRetries is how many times a transient failure (network, 429, 529
// overloaded, 5xx) is retried before giving up. Long sessions hit transient
// overloads; one blip must not kill an hour-long turn.
const maxTransientRetries = 5

// Backoff bounds — vars so tests can shrink them.
var (
	retryBaseDelay = 1 * time.Second
	retryMaxDelay  = 30 * time.Second
)

// isTransientStatus reports whether an HTTP status is worth retrying: rate
// limit (429), anthropic overload (529), and any 5xx.
func isTransientStatus(code int) bool {
	return code == 429 || code == 529 || (code >= 500 && code <= 599)
}

// backoffDelay is exponential backoff (1s, 2s, 4s, …) capped at retryMaxDelay.
func backoffDelay(attempt int) time.Duration {
	d := retryBaseDelay << attempt
	if d > retryMaxDelay || d <= 0 {
		d = retryMaxDelay
	}
	return d
}

// parseRetryAfter reads a Retry-After header expressed in seconds (0 if absent
// or non-numeric — HTTP-date form is not honored, exponential backoff covers it).
func parseRetryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// doWithAuthRetry executes a request. It retries once on 401 (fresh token) and
// up to maxTransientRetries on transient failures — network errors, 429, 529
// (overloaded), and 5xx — with backoff (honoring Retry-After). Transient
// retries happen BEFORE the caller reads the body, so no partial stream is
// emitted. This is what lets a long turn ride out an overload rather than die.
func (a *Anthropic) doWithAuthRetry(ctx context.Context, build func(apiKey string) (*http.Request, error)) (*http.Response, string, error) {
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, "", fmt.Errorf("resolve token: %w", err)
	}
	authRetried := false
	var lastErr error
	var delay time.Duration
	for attempt := 0; attempt <= maxTransientRetries; attempt++ {
		if delay > 0 {
			if !sleepCtx(ctx, delay) {
				return nil, apiKey, ctx.Err()
			}
			delay = 0
		}
		req, err := build(apiKey)
		if err != nil {
			return nil, apiKey, err
		}
		resp, err := a.HTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil { // cancel/timeout: not transient
				return nil, apiKey, fmt.Errorf("http: %w", err)
			}
			lastErr = fmt.Errorf("http: %w", err)
			delay = backoffDelay(attempt)
			slog.Warn("anthropic request failed, retrying", "attempt", attempt+1, "err", err)
			continue
		}
		// 401: invalidate + one retry with a fresh token (a free attempt —
		// it does not consume the transient budget).
		if resp.StatusCode == http.StatusUnauthorized && !authRetried {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			authRetried = true
			ierr := a.auth.Invalidate(apiKey)
			newKey, rerr := a.auth.Resolve()
			if rerr != nil {
				return nil, apiKey, fmt.Errorf("resolve after 401: %w", errors.Join(ierr, rerr))
			}
			if newKey == apiKey {
				if ierr != nil {
					return nil, apiKey, fmt.Errorf("anthropic 401: invalidate failed: %w", ierr)
				}
				return nil, apiKey, fmt.Errorf("anthropic 401: token unchanged after invalidate")
			}
			apiKey = newKey
			attempt-- // don't count the auth retry
			continue
		}
		if isTransientStatus(resp.StatusCode) {
			ra := parseRetryAfter(resp.Header)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("anthropic %d (transient)", resp.StatusCode)
			if ra > 0 {
				delay = ra
			} else {
				delay = backoffDelay(attempt)
			}
			slog.Warn("anthropic transient status, retrying", "status", resp.StatusCode, "attempt", attempt+1)
			continue
		}
		return resp, apiKey, nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, apiKey, fmt.Errorf("anthropic: giving up after %d attempts: %w", maxTransientRetries+1, lastErr)
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

// Fingerprint hashes the encoder config.
func (a *Anthropic) Fingerprint() string {
	rr := a.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return "anthropic/" + rr + "/v4"
}

func (a *Anthropic) SetModel(model string) {
	a.mu.Lock()
	a.Model = model
	a.mu.Unlock()
}

func decodeNativeMessage(nm nativeMessage) message.Message {
	// model/provider are not on the IR message — they live in the
	// chalkboard (system.model / system.provider), derived on read.
	m := message.Message{
		Role: message.Role(nm.Role),
	}
	for _, b := range nm.Content {
		switch b.Type {
		case "text":
			// Skip empty blocks so the sealed message matches the in-flight
			// asm (which never creates a node for empty text/thinking); keeping
			// them shifts later block indices and duplicates the live render.
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			m.Content = append(m.Content, message.Content{Type: message.ContentProse, Text: b.Text})
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			m.Content = append(m.Content, message.Content{Type: message.ContentThinking, Text: b.Thinking})
		case "tool_use":
			args, _ := b.Input.(map[string]interface{})
			m.Content = append(m.Content, message.Content{
				Type: message.ContentToolInvoke, ToolCallID: b.ID, ToolName: b.Name, Arguments: args,
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
		m.StopReason = message.StopToolInvoke
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

type nativeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    []systemBlock   `json:"system,omitempty"`
	Messages  []nativeMessage `json:"messages"`
	Tools     []nativeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
	Thinking  *thinkingParam  `json:"thinking,omitempty"`
}

type thinkingParam struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
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

// encode projects one IR message to native wire bytes.
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

// renderMessage produces the wire shape.
func (a *Anthropic) renderMessage(msg message.Message, prevSnap *chalkboard.Snapshot) (nativeMessage, bool) {
	switch msg.Role {
	case message.RoleUser:
		var blocks []nativeBlock
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
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
			case message.ContentProse:
				blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
			case message.ContentThinking:
				blocks = append(blocks, nativeBlock{Type: "thinking", Thinking: c.Text})
			case message.ContentToolInvoke:
				// Force non-nil input (API requires it).
				var input interface{}
				if len(c.Arguments) == 0 {
					input = json.RawMessage("{}")
				} else {
					input = c.Arguments
				}
				blocks = append(blocks, nativeBlock{
					Type: "tool_use", ID: c.ToolCallID, Name: c.ToolName, Input: input,
				})
			}
		}
		if len(blocks) == 0 {
			return nativeMessage{}, false
		}
		return nativeMessage{Role: "assistant", Content: blocks}, true

	case message.RoleSystemInterrupt:
		// Surrogate: synthetic user-role tool_result blocks per
		// dangling tool_use_id, IsError=true. Anthropic requires each
		// tool_use to be followed by a tool_result; this satisfies
		// that for an interrupted turn.
		var blocks []nativeBlock
		for _, c := range msg.Content {
			if c.Type != message.ContentInterrupt || c.ToolCallID == "" {
				continue
			}
			text := c.Text
			if text == "" {
				text = "(tool execution was interrupted)"
			}
			blocks = append(blocks, nativeBlock{
				Type: "tool_result", ToolUseID: c.ToolCallID,
				IsError: true,
				Content: []nativeBlock{{Type: "text", Text: text}},
			})
		}
		if len(blocks) == 0 {
			return nativeMessage{}, false
		}
		return nativeMessage{Role: "user", Content: blocks}, true
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

// systemBlocks builds the system prefix: preamble + credo.
//
// The credo lives on the chalkboard at `system.credo`. It may be a
// bare string (inline TOML) or a ContentEnvelope object emitted by
// the outfitter's fileName loader: {content, frontmatter, filePath}.
// Prefer content, fall back to frontmatter, then to a bare string.
func systemBlocks(snapshot chalkboard.Snapshot, oauth bool) []systemBlock {
	var out []systemBlock
	systemText := readCredo(snapshot)
	if oauth {
		out = append(out, systemBlock{Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			out = append(out, systemBlock{Type: "text", Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code — follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		out = append(out, systemBlock{Type: "text", Text: systemText})
	}
	return out
}

// readCredo extracts the credo text from a chalkboard snapshot,
// handling both the bare-string and ContentEnvelope shapes.
func readCredo(snapshot chalkboard.Snapshot) string {
	raw, ok := snapshot["system.credo"]
	if !ok {
		return ""
	}
	var env struct {
		Content     string `json:"content,omitempty"`
		Frontmatter string `json:"frontmatter,omitempty"`
	}
	if json.Unmarshal(raw, &env) == nil && (env.Content != "" || env.Frontmatter != "") {
		if env.Content != "" {
			return env.Content
		}
		return env.Frontmatter
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// projectMessagesWithModel assembles a nativeRequest from cached
// per-message bytes.
func (a *Anthropic) projectMessagesWithModel(perMessage [][]json.RawMessage, snapshot chalkboard.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, error) {
	return a.projectMessagesWithLTs(perMessage, nil, snapshot, tools, maxTokens, oauth, model)
}

// projectMessagesWithLTs is the assembler with per-message LTs.
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
		// TODO: put tools on the chalkboard as an ordered list.
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
	applyThinking(&req, snapshot, model)
	return req, nil
}

// applyMessageTags reads system.tags and applies per-message
// cache_control overrides keyed by logical time.
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

// markCacheBreakpoints attaches cache_control to the last block of
// each cacheable region.
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

// Send drives one turn: catch up cache, POST, stream SSE, land
// the assistant message in figLog + cache.
func (a *Anthropic) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	cache := a.cacheFor(in.AriaID)
	perMessage, lts := a.catchUp(in.AriaID, in.FigLog, cache, in.Chalkboard)
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
		// Broken stream: drop partial data.
		return err
	}
	if len(nm.Content) == 0 {
		return nil
	}

	// Land the assistant message: figLog → push figaro → cache.
	msg := decodeNativeMessage(nm)
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return fmt.Errorf("append assistant: %w", err)
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)

	if cache != nil {
		// Re-encode for cache (strips inbound-only fields).
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

// TransportFn executes a single HTTP request given a serialized body.
type TransportFn func(ctx context.Context, body []byte) (*http.Response, error)

// SendWithTransport is like Send but delegates the HTTP call to fn
// instead of using the built-in auth retry + Anthropic endpoint.
// oauth controls whether the Claude Code identity preamble is injected
// into the system prompt (false for Copilot, true for Anthropic OAuth).
func (a *Anthropic) SendWithTransport(ctx context.Context, in provider.SendInput, bus provider.Bus, oauth bool, fn TransportFn) error {
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	cache := a.cacheFor(in.AriaID)
	perMessage, lts := a.catchUp(in.AriaID, in.FigLog, cache, in.Chalkboard)
	if len(perMessage) == 0 {
		return fmt.Errorf("empty context")
	}
	model := a.resolveModel(in.Snapshot)

	req, err := a.projectMessagesWithLTs(perMessage, lts, in.Snapshot, in.Tools, in.MaxTokens, oauth, model)
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := fn(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot API error %d: %s", resp.StatusCode, string(errBody))
	}

	nm, err := a.drainSSE(ctx, resp.Body, model, bus)
	if err != nil {
		return err
	}
	if len(nm.Content) == 0 {
		return nil
	}

	msg := decodeNativeMessage(nm)
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return fmt.Errorf("append assistant: %w", err)
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)

	if cache != nil {
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

// catchUp encodes uncached figLog entries and returns per-message
// wire bytes.
func (a *Anthropic) catchUp(aria string, figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], chalk provider.Chalkboard) ([][]json.RawMessage, []uint64) {
	fp := a.Fingerprint()
	entries := store.Snapshot(figLog)
	a.mu.Lock()
	sc := a.snapCache[aria]
	a.mu.Unlock()

	snap := chalkboard.Snapshot{}
	var perMessage [][]json.RawMessage
	var lts []uint64
	startIdx := 0
	if sc != nil && sc.nEntries <= len(entries) &&
		(sc.nEntries == 0 || entries[sc.nEntries-1].LT == sc.lastLT) {
		snap = sc.snap
		perMessage = sc.perMessage
		lts = sc.lts
		startIdx = sc.nEntries
	}
	for _, e := range entries[startIdx:] {
		msg := e.Payload
		msg.LogicalTime = e.LT
		if msg.Role == message.RoleGenesis {
			continue // structural birth message; never rendered
		}
		if chalk != nil {
			msg.Patches = chalk.PatchesAt(e.LT)
		}
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
	if aria != "" {
		var lastLT uint64
		if len(entries) > 0 {
			lastLT = entries[len(entries)-1].LT
		}
		a.mu.Lock()
		if a.snapCache == nil {
			a.snapCache = map[string]*translationSnapshot{}
		}
		a.snapCache[aria] = &translationSnapshot{
			snap:       snap,
			perMessage: perMessage,
			lts:        lts,
			nEntries:   len(entries),
			lastLT:     lastLT,
		}
		a.mu.Unlock()
	}
	return perMessage, lts
}

const sseReadTimeout = 5 * time.Minute

// drainSSE reads SSE events, folds deltas into an accumulator,
// and returns the final message at message_stop. Non-nil error
// means the partial message must be dropped.
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
		a.foldSSEEvent(ctx, eventType, data, &nm, &usage, &stopReason, bus)
		if eventType == "message_stop" {
			return finalizeClean()
		}
		if eventType == "error" {
			return nm, fmt.Errorf("sse error event: %s", string(data))
		}
	}
}

// foldSSEEvent updates the accumulator with one SSE event.
func (a *Anthropic) foldSSEEvent(ctx context.Context, eventType string, data []byte, nm *nativeMessage, usage *nativeUsage, stopReason *string, bus provider.Bus) {
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
			// Announce tool so CLI can show a spinner.
			bus.PushToolInvokeStart(block.ContentBlock.ID, block.ContentBlock.Name)
			figOtel.Event(ctx, "provider.tool_use.block_start",
				attribute.String("tool_call_id", block.ContentBlock.ID),
				attribute.String("tool_name", block.ContentBlock.Name),
			)
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
				bus.PushDelta(message.Content{Type: message.ContentProse, Text: d.Delta.Text})
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
				figOtel.Event(ctx, "provider.tool_use.first_input_delta",
					attribute.String("tool_call_id", b.ID),
					attribute.String("tool_name", b.Name),
					attribute.Int("bytes", len(d.Delta.PartialJSON)),
				)
			}
			if d.Delta.PartialJSON != "" {
				bus.PushToolInvokeDelta(b.ID, d.Delta.PartialJSON)
			}
		}
	case "content_block_stop":
		var stop struct{ Index int }
		if json.Unmarshal(data, &stop) != nil || stop.Index >= len(nm.Content) {
			return
		}
		b := &nm.Content[stop.Index]
		if b.Type == "tool_use" {
			var rawLen int
			if s, ok := b.Input.(string); ok && s != "" {
				rawLen = len(s)
				var args map[string]interface{}
				if json.Unmarshal([]byte(s), &args) == nil {
					b.Input = args
				}
			} else if b.Input == nil {
				b.Input = map[string]interface{}{}
			}
			figOtel.Event(ctx, "provider.tool_use.block_stop",
				attribute.String("tool_call_id", b.ID),
				attribute.String("tool_name", b.Name),
				attribute.Int("input_bytes", rawLen),
			)
			// Speculative dispatch: signal the harness that this tool's
			// input is fully decoded so it can start executing now,
			// without waiting for the rest of the stream.
			if args, ok := b.Input.(map[string]interface{}); ok {
				bus.PushToolReady(message.Content{
					Type:       message.ContentToolInvoke,
					ToolCallID: b.ID,
					ToolName:   b.Name,
					Arguments:  args,
				})
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

	case "error":
		*stopReason = "error"
	}
}

// applyThinking enables extended thinking on the request when the
// chalkboard has system.thinking_budget or system.thinking_effort set.
func applyThinking(req *nativeRequest, snap chalkboard.Snapshot, model string) {
	budgetRaw, _ := snap["system.thinking_budget"]
	effortRaw, _ := snap["system.thinking_effort"]

	var budget int
	if len(budgetRaw) > 0 {
		json.Unmarshal(budgetRaw, &budget)
		if budget == 0 {
			var s string
			if json.Unmarshal(budgetRaw, &s) == nil {
				fmt.Sscanf(s, "%d", &budget)
			}
		}
	}
	var effort string
	if len(effortRaw) > 0 {
		json.Unmarshal(effortRaw, &effort)
	}

	if budget <= 0 && effort == "" {
		return
	}

	if isAdaptiveModel(model) {
		if effort == "" {
			effort = "high"
		}
		req.Thinking = &thinkingParam{Type: "enabled", BudgetTokens: 10000, Display: "summarized"}
		return
	}

	if budget <= 0 {
		budget = 10000
	}
	if budget < 1024 {
		budget = 1024
	}
	req.Thinking = &thinkingParam{Type: "enabled", BudgetTokens: budget, Display: "summarized"}
	if req.MaxTokens <= budget {
		req.MaxTokens = budget + 4096
	}
}

func isAdaptiveModel(model string) bool {
	for _, frag := range []string{"opus-4.6", "opus-4.7", "opus-4.8", "sonnet-4.6", "sonnet-4.7", "sonnet-5"} {
		if strings.Contains(model, frag) {
			return true
		}
	}
	return false
}
