// Package anthropic implements the Provider for the Anthropic Messages API.
package anthropic

import (
	"bufio"
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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/auth"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropicmodels"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
)

const (
	providerName      = "anthropic"
	apiVersion        = "2023-06-01"
	claudeCodeVersion = anthropicmodels.ClaudeCodeVersion
)

type Anthropic struct {
	auth             auth.TokenResolver
	mu               sync.Mutex
	Model            string
	MaxTokens        int
	HTTPClient       *http.Client
	ReminderRenderer string // "tag" (default) or "tool"

	// Route is not part of Fingerprint: re-pointing at a proxy must not
	// invalidate an aria's translation cache.
	Route provider.Route

	Templates *template.Template

	CacheOpen      func(aria string) (store.Log[[]json.RawMessage], error)
	CacheNamespace string
	cache          store.Log[[]json.RawMessage]

	windows anthropicmodels.Catalog
}

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
		HTTPClient:       &http.Client{Timeout: 10 * time.Minute, Transport: &wirelog.Transport{Inner: http.DefaultTransport}},
		ReminderRenderer: rr,
		CacheOpen:        cacheOpen,
		CacheNamespace:   providerName,
		Route:            provider.WithBaseURLOverride(provider.AnthropicDirect(), providerName),
	}, nil
}

// A zero-value Route must not mean "no caching".
func (a *Anthropic) route() provider.Route {
	if a.Route.BaseURL == "" {
		return provider.WithBaseURLOverride(provider.AnthropicDirect(), providerName)
	}
	return a.Route
}

func (a *Anthropic) cacheFor(aria string) (store.Log[[]json.RawMessage], error) {
	if aria == "" || a.CacheOpen == nil {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil {
		return a.cache, nil
	}
	s, err := a.CacheOpen(aria)
	if err != nil {
		return nil, fmt.Errorf("anthropic open translator log: %w", err)
	}
	if !a.invalidateIfStale(s) {
		return nil, fmt.Errorf("anthropic translator log invalidation failed")
	}
	a.cache = s
	return s, nil
}

func (a *Anthropic) invalidateIfStale(s store.Log[[]json.RawMessage]) bool {
	want := a.Fingerprint()
	stored, cleared, err := provider.ClearStaleTranslationCache(s, want)
	if err != nil {
		slog.Warn("anthropic clear stale cache", "stored", stored, "current", want, "err", err)
		return false
	}
	if cleared {
		slog.Info("anthropic cleared stale cache", "stored", stored, "current", want)
	}
	return true
}

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

const maxTransientRetries = 5

var (
	retryBaseDelay = 1 * time.Second
	retryMaxDelay  = 30 * time.Second
)

func isTransientStatus(code int) bool {
	return code == 429 || code == 529 || (code >= 500 && code <= 599)
}

// apiErrorMessage reads {"error":{"type","message"}}, falling back to the raw body.
func apiErrorMessage(status int, body []byte) string {
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		if parsed.Error.Type != "" {
			return fmt.Sprintf("%s: %s (%d)", parsed.Error.Type, parsed.Error.Message, status)
		}
		return fmt.Sprintf("%s (%d)", parsed.Error.Message, status)
	}
	if len(body) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, string(body))
}

func backoffDelay(attempt int) time.Duration {
	d := retryBaseDelay << attempt
	if d > retryMaxDelay || d <= 0 {
		d = retryMaxDelay
	}
	return d
}

// parseRetryAfter honours the seconds form only; HTTP-date yields 0.
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

// doWithAuthRetry retries once on 401 with a fresh token and up to
// maxTransientRetries on transient failures, before any body is read.
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
		// The 401 retry does not consume the transient budget.
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
		req, err := http.NewRequestWithContext(ctx, "GET", a.route().ModelsURL()+"?limit=100", nil)
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
			ID             string `json:"id"`
			DisplayName    string `json:"display_name"`
			MaxInputTokens int    `json:"max_input_tokens"`
			MaxTokens      int    `json:"max_tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	var models []provider.ModelInfo
	for _, m := range result.Data {
		a.windows.Learn(m.ID, m.MaxInputTokens)
		models = append(models, provider.ModelInfo{
			ID:            m.ID,
			Name:          m.DisplayName,
			Provider:      providerName,
			ContextWindow: m.MaxInputTokens,
			MaxTokens:     m.MaxTokens,
		})
	}
	return models, nil
}

// ContextLimit prefers system.max_context_tokens, then the learned window,
// then the static table (0 when unknown). No network I/O.
func (a *Anthropic) ContextLimit(model string, snapshot form.Snapshot) int {
	if model == "" {
		a.mu.Lock()
		model = a.Model
		a.mu.Unlock()
	}
	return a.windows.ContextLimit(model, snapshot)
}

func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

func (a *Anthropic) Name() string { return providerName }

func (a *Anthropic) Fingerprint() string {
	rr := a.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return "anthropic/" + rr + "/v7"
}

func (a *Anthropic) SetModel(model string) {
	a.mu.Lock()
	a.Model = model
	a.mu.Unlock()
}

// validNativeBlock gates both the IR decoder and the native cache: an empty
// text/thinking block shifts later block indices against the live render.
func validNativeBlock(b nativeBlock) bool {
	switch b.Type {
	case "text":
		return strings.TrimSpace(b.Text) != ""
	case "thinking":
		return strings.TrimSpace(b.Thinking) != ""
	case "redacted_thinking":
		return b.Data != ""
	case "tool_use":
		return cacheableToolUse(b)
	case "":
		return false
	}
	return true
}

// cacheableToolUse is the single predicate for IR and cache: a call with
// partial-JSON input must vanish from both, or a result outlives its tool_use.
func cacheableToolUse(b nativeBlock) bool {
	if b.ID == "" {
		return false
	}
	_, ok := b.Input.(map[string]interface{})
	return ok
}

// cacheableNativeBlock is wider than validNativeBlock for thinking: a signed
// empty-summary block must replay to lead a tool-use assistant.
func cacheableNativeBlock(b nativeBlock) (keep, fatal bool) {
	switch b.Type {
	case "text":
		return strings.TrimSpace(b.Text) != "", false
	case "thinking":
		// An unsigned thinking block replays as a 400 forever; the IR keeps
		// its text for the transcript.
		return b.Signature != "", false
	case "redacted_thinking":
		return b.Data != "", false
	case "tool_use":
		return cacheableToolUse(b), false
	case "":
		return false, false
	}
	return true, false
}

func decodeNativeMessage(nm nativeMessage) message.Message {
	m := message.Message{
		Role: message.RoleFromWire(nm.Role),
	}
	for _, b := range nm.Content {
		if !validNativeBlock(b) {
			continue
		}
		switch b.Type {
		case "text":
			m.Content = append(m.Content, message.Content{Type: message.ContentProse, Text: b.Text})
		case "thinking":
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
	m.StopReason = provider.AnthropicStopReason(nm.StopReason)
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
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    []systemBlock `json:"system,omitempty"`
	// Messages are translator rows spliced verbatim; only cache_control
	// marking decodes a row, and only the rows it marks.
	Messages     []json.RawMessage `json:"messages"`
	Tools        []nativeTool      `json:"tools,omitempty"`
	Stream       bool              `json:"stream"`
	Thinking     *thinkingParam    `json:"thinking,omitempty"`
	OutputConfig *outputConfig     `json:"output_config,omitempty"`
}

type outputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type thinkingParam struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
	// TTL is "" (provider default) or "1h"; a retention in Type is rejected.
	TTL string `json:"ttl,omitempty"`
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
	Data         string        `json:"data,omitempty"`
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Input        interface{}   `json:"input,omitempty"`
	ToolUseID    string        `json:"tool_use_id,omitempty"`
	IsError      bool          `json:"is_error,omitempty"`
	Content      interface{}   `json:"content,omitempty"`
	Source       interface{}   `json:"source,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON keeps the thinking key on an empty signed block; omitempty
// would drop it and 400 the replay.
func (b nativeBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "thinking" {
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature,omitempty"`
		}{b.Type, b.Thinking, b.Signature})
	}
	type alias nativeBlock
	return json.Marshal(alias(b))
}

type nativeTool struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	InputSchema  interface{}   `json:"input_schema"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

func (a *Anthropic) encode(msg message.Message, prevSnapshot form.Snapshot) ([]json.RawMessage, error) {
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

func (a *Anthropic) renderMessage(msg message.Message, prevSnap *form.Snapshot) (nativeMessage, bool) {
	switch msg.Role {
	case message.RoleInput:
		toolImages := message.ToolImagesByCall(msg.Content)
		var blocks []nativeBlock
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
			case message.ContentImage:
				// Claimed images render inside their tool_result below.
				if _, claimed := toolImages[c.ToolCallID]; claimed && c.ToolCallID != "" {
					continue
				}
				blocks = append(blocks, imageBlock(c))
			case message.ContentToolResult:
				text := c.Text
				if text == "" {
					text = "(empty)"
				}
				inner := []nativeBlock{{Type: "text", Text: text}}
				for _, img := range toolImages[c.ToolCallID] {
					inner = append(inner, imageBlock(img))
				}
				blocks = append(blocks, nativeBlock{
					Type: "tool_result", ToolUseID: c.ToolCallID,
					IsError: c.IsError,
					Content: inner,
				})
			}
		}
		// Taken before renderPatchBlocks advances prevSnap: every dialect
		// reads incantations from the pre-patch board.
		board := *prevSnap
		patchBlocks, advanced := a.renderPatchBlocks(msg.Patches, board)
		*prevSnap = advanced
		reminders := patchBlocks
		for _, text := range provider.StudyReminderTexts(msg, board) {
			reminders = append(reminders, nativeBlock{Type: "text", Text: text})
		}
		for _, text := range provider.ForkReminderTexts(msg, board) {
			reminders = append(reminders, nativeBlock{Type: "text", Text: text})
		}
		blocks = placeReminders(blocks, reminders)
		if len(blocks) == 0 {
			return nativeMessage{}, false
		}
		// The API takes tool_result before text in a user message.
		return nativeMessage{Role: "user", Content: resultsFirst(blocks)}, true

	case message.RoleOutput:
		var blocks []nativeBlock
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
			case message.ContentThinking:
				// The IR carries no signature; unsigned thinking is a 400.
			case message.ContentToolInvoke:
				// The API requires non-null input.
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
		// Every tool_use needs a tool_result; an interrupted turn gets
		// error results.
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

func (a *Anthropic) renderPatchBlocks(patches []message.Patch, snap form.Snapshot) ([]nativeBlock, form.Snapshot) {
	if a.Templates != nil && a.ReminderRenderer == "tool" {
		slog.Warn("anthropic: reminder_renderer=tool not supported inline; using tag")
	}
	var out []nativeBlock
	snap = form.FoldRender(snap, patches, a.Templates,
		func(r form.RenderedEntry) {
			out = append(out, nativeBlock{Type: "text", Text: fmt.Sprintf(
				"<system-reminder name=\"%s\">\n%s\n</system-reminder>", escapeAttr(r.Key), r.Body)})
		},
		func(err error) { slog.Warn("anthropic: render patch", "err", err) })
	return out, snap
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

func systemBlocks(snapshot form.Snapshot, oauth bool) []systemBlock {
	var out []systemBlock
	systemText := readCredo(snapshot)
	if oauth {
		out = append(out, systemBlock{Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			out = append(out, systemBlock{Type: "text", Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code: follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		out = append(out, systemBlock{Type: "text", Text: systemText})
	}
	return out
}

func readCredo(snapshot form.Snapshot) string {
	raw, ok := snapshot.Get("system.credo")
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

// projectMessagesWithModel materializes the request; the send path uses
// projectRequest's row sequence instead.
func (a *Anthropic) projectMessagesWithModel(perMessage [][]json.RawMessage, snapshot form.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, error) {
	return a.projectMessagesWithLTs(perMessage, nil, snapshot, tools, maxTokens, oauth, model)
}

func (a *Anthropic) projectMessagesWithLTs(perMessage [][]json.RawMessage, lts []uint64, snapshot form.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, error) {
	req, rows, err := a.projectRequest(provider.PerMessageRows(perMessage, lts), snapshot, tools, maxTokens, oauth, model)
	if err != nil {
		return req, err
	}
	req.Messages, _ = provider.CollectRows(rows)
	return req, nil
}

// projectRequest builds everything but the messages and returns the row
// sequence that splices into it; in-message cache marks ride the sequence.
func (a *Anthropic) projectRequest(src provider.RowSeq, snapshot form.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, provider.RowSeq, error) {
	if maxTokens == 0 {
		maxTokens = a.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}
	req := nativeRequest{
		Model: model, MaxTokens: maxTokens, Stream: true,
		System: systemBlocks(snapshot, oauth),
		// TODO: put tools on the form as an ordered list.
		Tools: projectTools(tools),
	}

	// Row-dropping passes run before any marking addresses a row.
	rows := coalesceRowsSeq(pairToolCallsSeq(dropDuplicateResultsSeq(src)))

	var tailMark *cacheControl
	if policy := provider.ResolveCachePolicy(snapshot); !policy.Off() {
		route := a.route()
		// Not gated on the model's minimum cacheable size: a marker below
		// it is ignored, not charged.
		if plan := route.MarkPlan(provider.ResolveMarkMode(snapshot)); plan.Blocks {
			tailMark = markCacheBreakpoints(&req, policy, plan, route.Caps)
		}
	}
	rows = markRowsSeq(rows, tailMark, messageTags(snapshot, a.route().Caps))
	applyThinking(&req, snapshot, model)
	return req, rows, nil
}

// messageTags reads system.tags into per-LT cache_control overrides, applied
// to the last row carrying each logical time.
func messageTags(snapshot form.Snapshot, caps provider.CacheCaps) map[uint64]*cacheControl {
	raw, ok := snapshot.Get("system.tags")
	if !ok || len(raw) == 0 {
		return nil
	}
	var tags map[string]struct {
		CacheControl string `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil
	}
	if len(tags) == 0 {
		return nil
	}
	out := make(map[uint64]*cacheControl, len(tags))
	for key, tag := range tags {
		if tag.CacheControl == "" {
			continue
		}
		lt, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			continue
		}
		policy := provider.ParseCachePolicy(tag.CacheControl)
		if policy.Off() {
			continue
		}
		out[lt] = controlFor(policy, caps)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// controlFor drops a ttl the route will not honour.
func controlFor(p provider.CachePolicy, caps provider.CacheCaps) *cacheControl {
	cc := &cacheControl{Type: p.Type}
	if caps.TTL {
		cc.TTL = p.TTL
	}
	return cc
}

// markCacheBreakpoints spends 3 of Anthropic's 4 breakpoints: last system
// block, last tool, and the tail message; the fourth is left for a gateway
// or the per-fork long-retention marker.
func markCacheBreakpoints(req *nativeRequest, policy provider.CachePolicy, plan provider.MarkPlan, caps provider.CacheCaps) *cacheControl {
	cc := controlFor(policy, caps)
	budget := caps.MaxMarkers
	if budget > provider.AutoCacheBreakpoints {
		budget = provider.AutoCacheBreakpoints
	}
	spend := func() bool {
		if budget <= 0 {
			return false
		}
		budget--
		return true
	}
	if n := len(req.System); n > 0 && spend() {
		req.System[n-1].CacheControl = cc
	}
	if n := len(req.Tools); n > 0 && spend() {
		req.Tools[n-1].CacheControl = cc
	}
	if !plan.Tail {
		return nil
	}
	// The tail marker is spent by the assembler on the last row.
	if !spend() {
		return nil
	}
	return cc
}

// rowMessage decodes one row; not for the splice path.
func rowMessage(row json.RawMessage) nativeMessage {
	var m nativeMessage
	_ = json.Unmarshal(row, &m)
	return m
}

// markRowTail attaches cache_control to a row's last content block; the only
// edit that reaches inside a row.
func markRowTail(row *json.RawMessage, cc *cacheControl) {
	var m nativeMessage
	if err := json.Unmarshal(*row, &m); err != nil {
		return // a row we cannot read is a row we must not rewrite
	}
	k := len(m.Content)
	if k == 0 {
		return
	}
	m.Content[k-1].CacheControl = cc
	marked, err := json.Marshal(m)
	if err != nil {
		return
	}
	*row = marked
}

// countCacheMarkers counts cache_control markers; a fifth is a 400.
func countCacheMarkers(req nativeRequest) int {
	n := 0
	for _, s := range req.System {
		if s.CacheControl != nil {
			n++
		}
	}
	for _, t := range req.Tools {
		if t.CacheControl != nil {
			n++
		}
	}
	for _, row := range req.Messages {
		for _, c := range rowMessage(row).Content {
			if c.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

func (a *Anthropic) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	ctx = wirelog.WithAria(ctx, in.AriaID)
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	cache, err := a.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	src, err := a.catchUp(in.FigLog, cache, in.Form, in.Studies)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("empty context")
	}

	apiKey, err := a.auth.Resolve()
	if err != nil {
		return fmt.Errorf("resolve token: %w", err)
	}
	model := a.resolveModel(in.Snapshot)

	req, rows, err := a.projectRequest(src, in.Snapshot, in.Tools, in.MaxTokens, isOAuthToken(apiKey), model)
	if err != nil {
		return err
	}
	body, err := provider.NewRequestBody(bodyFuncSeq(req, rows), provider.StreamsRequestBody(in.Snapshot))
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	resp, _, err := a.doWithAuthRetry(ctx, func(token string) (*http.Request, error) {
		httpReq, herr := http.NewRequestWithContext(ctx, "POST", a.route().MessagesURL(), nil)
		if herr != nil {
			return nil, fmt.Errorf("create request: %w", herr)
		}
		body.Attach(httpReq)
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
		return fmt.Errorf("anthropic: %s", apiErrorMessage(resp.StatusCode, errBody))
	}

	return a.land(a.drainSSE(ctx, resp.Body, model, bus))(providerName, bus)
}

// land settles a drained stream. A cancelled turn hands over what the
// accumulator holds (its native payload keeps the signed thinking); any
// other stream error drops it. A clean close that is not a reply hands over
// the partial and fails the send with the wire's stop reason.
func (a *Anthropic) land(nm nativeMessage, err error) func(label string, bus provider.Bus) error {
	return func(label string, bus provider.Bus) error {
		if err != nil {
			if !errors.Is(err, context.Canceled) || len(nm.Content) == 0 {
				return err
			}
			nm.StopReason = string(message.StopAborted)
			if perr := a.handOver(nm, bus); perr != nil {
				return perr
			}
			return err
		}
		if fault := provider.CheckCompletion(label, decodeNativeMessage(nm), nm.StopReason); fault != nil {
			if len(nm.Content) == 0 {
				return fault
			}
			if perr := a.handOver(nm, bus); perr != nil {
				return perr
			}
			return fault
		}
		return a.handOver(nm, bus)
	}
}

// handOver is the single path to the bus for whole, partial and faulted
// messages; the fig IR side appends, since only it has the LT.
func (a *Anthropic) handOver(nm nativeMessage, bus provider.Bus) error {
	msg := decodeNativeMessage(nm)
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	bus.PushMessageEnd(string(msg.StopReason))
	native, err := a.assistantCacheNative(nm)
	if err != nil {
		return fmt.Errorf("anthropic cache assistant: %w", err)
	}
	bus.PushFigaro(msg, native)
	return nil
}

type TransportFn func(ctx context.Context, body *provider.RequestBody) (*http.Response, error)

// SendWithTransport delegates the HTTP call to fn; oauth injects the Claude
// Code identity preamble.
func (a *Anthropic) SendWithTransport(ctx context.Context, in provider.SendInput, bus provider.Bus, oauth bool, fn TransportFn) error {
	ctx = wirelog.WithAria(ctx, in.AriaID)
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	cache, err := a.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	src, err := a.catchUp(in.FigLog, cache, in.Form, in.Studies)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("empty context")
	}
	model := a.resolveModel(in.Snapshot)

	req, rows, err := a.projectRequest(src, in.Snapshot, in.Tools, in.MaxTokens, oauth, model)
	if err != nil {
		return err
	}
	body, err := provider.NewRequestBody(bodyFuncSeq(req, rows), provider.StreamsRequestBody(in.Snapshot))
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	resp, err := fn(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot: %s", apiErrorMessage(resp.StatusCode, errBody))
	}

	return a.land(a.drainSSE(ctx, resp.Body, model, bus))("copilot", bus)
}

func (a *Anthropic) assistantCacheNative(msg nativeMessage) (provider.AssistantCache, error) {
	msg.StopReason = ""
	msg.Model = ""
	msg.Usage = nil
	var content []nativeBlock
	for _, b := range msg.Content {
		keep, fatal := cacheableNativeBlock(b)
		if fatal {
			content = nil
			break
		}
		if keep {
			content = append(content, b)
		}
	}
	if len(content) == 0 {
		return provider.AssistantCache{
			Namespace: a.CacheNamespace, Fingerprint: a.Fingerprint(),
		}, nil
	}
	msg.Content = content
	raw, err := json.Marshal(msg)
	if err != nil {
		return provider.AssistantCache{}, fmt.Errorf("marshal native assistant cache: %w", err)
	}
	return provider.AssistantCache{
		Namespace: a.CacheNamespace, Payload: []json.RawMessage{raw}, Fingerprint: a.Fingerprint(),
	}, nil
}

func (a *Anthropic) assistantCache(msg message.Message) (provider.AssistantCache, error) {
	encoded, err := a.encode(msg, form.Snapshot{})
	if err != nil {
		return provider.AssistantCache{}, err
	}
	return provider.AssistantCache{
		Namespace: a.CacheNamespace, Payload: encoded, Fingerprint: a.Fingerprint(),
	}, nil
}

func (a *Anthropic) resolveModel(snap form.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return *v
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Model
}

// catchUp translates the untranslated delta and returns a walk over the rows
// on disk, never a slice of them. Nil means the log has no rows.
func (a *Anthropic) catchUp(figLog store.Log[message.Message], rows store.Log[[]json.RawMessage], form provider.Form, studies map[string]provider.Form) (provider.RowSeq, error) {
	if _, err := provider.CatchUp(provider.CatchUpConfig{
		Log:         figLog,
		Translator:  rows,
		Form:        form,
		Studies:     studies,
		Fingerprint: a.Fingerprint(),
		Provider:    a.Name(),
		Encode:      a.encode,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Error("anthropic encode", "flt", lt, "err", err)
		},
		ReportWriteError: func(lt uint64, err error) {
			slog.Error("anthropic write row", "flt", lt, "err", err)
		},
	}); err != nil {
		// A send that cannot write its rows must not proceed.
		return nil, fmt.Errorf("anthropic catch up: %w", err)
	}
	to, ok := provider.TranslationTail(rows)
	if !ok {
		return nil, nil
	}
	return provider.TranslationRows(rows, to), nil
}

const sseReadTimeout = 5 * time.Minute

// drainSSE returns the accumulated message at message_stop; a non-nil error
// means the partial must be dropped.
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

	// An empty stop reason travels as-is; the decoder reads it as a fault.
	finalizeClean := func() (nativeMessage, error) {
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

func (a *Anthropic) foldSSEEvent(ctx context.Context, eventType string, data []byte, nm *nativeMessage, usage *nativeUsage, stopReason *string, bus provider.Bus) {
	switch eventType {
	case "content_block_start":
		var block struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type      string `json:"type"`
				ID        string `json:"id,omitempty"`
				Name      string `json:"name,omitempty"`
				Signature string `json:"signature,omitempty"`
				Data      string `json:"data,omitempty"`
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
			nm.Content[block.Index] = nativeBlock{Type: "thinking", Signature: block.ContentBlock.Signature}
		case "redacted_thinking":
			nm.Content[block.Index] = nativeBlock{Type: "redacted_thinking", Data: block.ContentBlock.Data}
		case "tool_use":
			nm.Content[block.Index] = nativeBlock{
				Type: "tool_use", ID: block.ContentBlock.ID, Name: block.ContentBlock.Name,
			}
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
				Signature   string `json:"signature,omitempty"`
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
		case "signature_delta":
			b.Signature += d.Delta.Signature
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
			// Speculative dispatch: the input is complete before the stream is.
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

func applyThinking(req *nativeRequest, snap form.Snapshot, model string) {
	budgetRaw, _ := snap.Get("system.thinking_budget")
	effortRaw, _ := snap.Get("system.thinking_effort")

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

	if provider.IsAdaptiveThinkingModel(model) {
		// budget_tokens and type "enabled" are a 400 on adaptive models.
		if effort == "" {
			effort = "high"
		}
		req.Thinking = &thinkingParam{Type: "adaptive", Display: "summarized"}
		req.OutputConfig = &outputConfig{Effort: effort}
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

func imageBlock(c message.Content) nativeBlock {
	return nativeBlock{
		Type: "image",
		Source: map[string]any{
			"type": "base64", "media_type": c.MimeType, "data": c.Data,
		},
	}
}

func (a *Anthropic) EncodeMessage(msg message.Message, prev form.Snapshot) ([]json.RawMessage, error) {
	return a.encode(msg, prev)
}

func (a *Anthropic) TranslatorChannel() string { return a.CacheNamespace }

// placeReminders puts reminders beside a person's words when there are any;
// in a results-only message they ride inside the last tool_result, where a
// top-level reminder would read as the user speaking and saying nothing.
func placeReminders(blocks, reminders []nativeBlock) []nativeBlock {
	if len(reminders) == 0 {
		return blocks
	}
	last := -1
	for i, b := range blocks {
		switch b.Type {
		case "tool_result":
			last = i
		case "text", "image":
			return append(blocks, reminders...)
		}
	}
	if last < 0 {
		return append(blocks, reminders...)
	}
	out := append([]nativeBlock(nil), blocks...)
	tr := out[last]
	// Result content may be a block list or a bare string; both widen to a list.
	var inner []nativeBlock
	switch c := tr.Content.(type) {
	case []nativeBlock:
		inner = append(inner, c...)
	case string:
		inner = append(inner, nativeBlock{Type: "text", Text: c})
	}
	tr.Content = append(inner, reminders...)
	out[last] = tr
	return out
}
