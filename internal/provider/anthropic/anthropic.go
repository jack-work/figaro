// Package anthropic implements the figaro Provider for the Anthropic Messages API.
//
// Direct HTTP+SSE, no SDK. Converts from message.Block IR to
// Anthropic's native format, using cached translations when
// available. Populates per-message translation on responses for
// cache-hit re-sends.
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
	"github.com/jack-work/figaro/internal/causal"
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

// New creates an Anthropic provider from the config section.
// authPath is the path to the provider's auth.json for OAuth credentials.
// hushClient is the hush client for token encryption/decryption (may be nil
// if using a static API key).
// Auth is resolved internally: if APIKey is set, it's used as a
// static key. Otherwise, OAuth via hush is used. The env var
// ANTHROPIC_API_KEY is checked as a fallback before OAuth.
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

// buildResolver returns the appropriate TokenResolver based on the
// available credentials. Priority: config api_key → env var → OAuth.
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

// Models fetches the list of available models from the Anthropic API.
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

// isOAuthToken detects OAuth access tokens by the "sk-ant-oat" infix.
func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

func (a *Anthropic) Name() string { return providerName }

// Fingerprint hashes the renderer config that affects wire bytes.
// Today only ReminderRenderer changes the per-tic system-reminder
// shape; the templates set is fixed across all arias for a given
// process lifetime, so we don't hash it (a future override-loading
// path would have to be folded in here).
func (a *Anthropic) Fingerprint() string {
	rr := a.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return "anthropic/" + rr + "/v1"
}

// Decode reverses projectMessages: nativeMessage wire bytes → IR.
// Reads stop_reason / usage / model when present (inbound metadata
// from consumeSSE). Inline <system-reminder> text blocks stay as
// plain text — the patch-rendering path is one-way.
func (a *Anthropic) Decode(raw []json.RawMessage) ([]message.Message, error) {
	out := make([]message.Message, 0, len(raw))
	for i, r := range raw {
		var nm nativeMessage
		if err := json.Unmarshal(r, &nm); err != nil {
			return nil, fmt.Errorf("anthropic decode [%d]: %w", i, err)
		}
		out = append(out, decodeNativeMessage(nm))
	}
	return out, nil
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

// projectAssistantMessage builds the wire shape for one finalized
// assistant Message. Extracted from the inline message_stop block
// of consumeSSE so the stub accumulator and any future per-message
// projector share one implementation. Mirrors the assistant case
// of projectMessages.
func projectAssistantMessage(msg message.Message) nativeMessage {
	out := nativeMessage{Role: "assistant"}
	for _, c := range msg.Content {
		switch c.Type {
		case message.ContentText:
			out.Content = append(out.Content, nativeBlock{Type: "text", Text: c.Text})
		case message.ContentThinking:
			out.Content = append(out.Content, nativeBlock{Type: "thinking", Thinking: c.Text})
		case message.ContentToolCall:
			out.Content = append(out.Content, nativeBlock{
				Type: "tool_use", ID: c.ToolCallID, Name: c.ToolName, Input: c.Arguments,
			})
		}
	}
	return out
}

// log writes to stderr with a provider prefix. Visible in the angelus log.
func log(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "anthropic: "+format+"\n", args...)
}

func (a *Anthropic) SetModel(model string) {
	a.mu.Lock()
	a.Model = model
	a.mu.Unlock()
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

// cacheControl marks a block as a prompt-cache breakpoint. Only "ephemeral"
// is supported today; Anthropic caches the prefix up to and including the
// marked block for the cache TTL (~5 min).
type cacheControl struct {
	Type string `json:"type"`
}

// systemBlock is one entry in the system prompt array. cache_control on
// the last system block caches the full system prefix.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type nativeMessage struct {
	Role    string        `json:"role"`
	Content []nativeBlock `json:"content"`
	// Inbound metadata captured by the SSE consumer. omitempty on the
	// outbound projection so encoded request bytes don't include them.
	StopReason string       `json:"stop_reason,omitempty"`
	Model      string       `json:"model,omitempty"`
	Usage      *nativeUsage `json:"usage,omitempty"`
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

// --- Projection: IR messages → native request ---

func (a *Anthropic) projectMessagesWithModel(msgs []message.Message, snapshot chalkboard.Snapshot, priorTranslations causal.Slice[message.ProviderTranslation], tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, provider.ProjectionSummary) {
	req := nativeRequest{
		Model: model, MaxTokens: maxTokens, Stream: true,
	}
	summary := provider.ProjectionSummary{
		Fingerprint: a.Fingerprint(),
	}

	// Build system prompt — always array form so cache_control can attach.
	// Sourced from chalkboard.system.prompt (set at bootstrap by the
	// agent loop). Ephemeral arias supply a synthesized snapshot.
	var systemText string
	if raw, ok := snapshot["system.prompt"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			systemText = s
		}
	}

	if oauth {
		// OAuth requires "You are Claude Code" as the first system block —
		// validated server-side for OAuth tokens.
		req.System = append(req.System, systemBlock{Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			// Our credo comes second, with an override instruction so the
			// model prioritizes the credo's identity.
			req.System = append(req.System, systemBlock{Type: "text", Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code — follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		req.System = append(req.System, systemBlock{Type: "text", Text: systemText})
	}

	// Stage E: emit chalkboard.system.skills as its own system block
	// after the credo. The catalog body is a markdown-rendered list
	// of {name, description, file_path}. Bodies aren't included —
	// the model uses the read tool to load a skill when it's
	// invoked.
	if raw, ok := snapshot["system.skills"]; ok && len(raw) > 0 {
		var entries []credo.SkillCatalogEntry
		if json.Unmarshal(raw, &entries) == nil && len(entries) > 0 {
			body := credo.FormatSkillCatalog(entries)
			if body != "" {
				req.System = append(req.System, systemBlock{Type: "text", Text: body})
			}
		}
	}

	// Capture system block bytes for the translation log BEFORE
	// markCacheBreakpoints attaches per-Send cache_control markers.
	// The persisted bytes must be cache_control-free; the markers
	// move based on which message is the leaf and would couple the
	// cached bytes to the position.
	if len(req.System) > 0 {
		summary.System = make([]json.RawMessage, len(req.System))
		for i, sb := range req.System {
			if raw, err := json.Marshal(sb); err == nil {
				summary.System[i] = raw
			}
		}
		summary.SystemFLT = lastSystemPromptFLT(msgs)
	}

	wireMsgs, perFLT := a.projectMessages(msgs, priorTranslations)
	req.Messages = wireMsgs
	summary.PerMessage = perFLT

	req.Tools = projectTools(tools)

	markCacheBreakpoints(&req)
	return req, summary
}

// lastSystemPromptFLT scans the figaro timeline backward and returns
// the LogicalTime of the most recent message whose Patches set
// chalkboard.system.prompt. Returns 0 when no message has set it
// (ephemeral arias that synthesize the snapshot externally).
//
// Compute-on-demand keeps the agent free of "current system flt"
// state; the figaro timeline is the source of truth.
func lastSystemPromptFLT(msgs []message.Message) uint64 {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, p := range msgs[i].Patches {
			if _, ok := p.Set["system.prompt"]; ok {
				return msgs[i].LogicalTime
			}
		}
	}
	return 0
}

// markCacheBreakpoints sets cache_control: ephemeral on the last block of
// each cacheable region of the request:
//
//  1. The last system block — caches the system prefix.
//  2. The last tool definition — caches system + tools.
//  3. The last content block of the second-to-last message — caches
//     system + tools + the conversation up through the leaf at the most
//     recent endTurn (i.e. everything that was on disk before the new
//     user prompt arrived).
//
// The third breakpoint is implicit: it follows from message ordering. If
// the conversation has fewer than two messages, no message-level
// breakpoint is set.
//
// Note: the OAuth + claude-code-20250219 beta path silently ignores
// client-controlled cache_control (verified by inspecting the
// cache_creation.ephemeral_*_input_tokens fields in the response, which
// stay at 0). On the API-key path, these breakpoints engage normally.
// The wiring is left in place so the cache works whenever the auth path
// allows it; correctness of the request bytes is verified by the unit
// tests in this package.
func markCacheBreakpoints(req *nativeRequest) {
	if n := len(req.System); n > 0 {
		req.System[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if n := len(req.Tools); n > 0 {
		req.Tools[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if n := len(req.Messages); n >= 2 {
		m := &req.Messages[n-2]
		if k := len(m.Content); k > 0 {
			m.Content[k-1].CacheControl = &cacheControl{Type: "ephemeral"}
		}
	}
}

// projectMessages converts the IR message stream to the wire shape.
// Patches on user-role Messages are rendered inline as system-reminder
// content blocks via the configured ReminderRenderer (Stage C, log
// unification). Tool results are carried as ContentToolResult blocks
// within the user-role tic that accumulated them.
//
// priorTranslations is parallel-indexed with msgs: priorTranslations.At(i)
// holds the cached wire-format projection for msgs[i] when known.
// An empty ProviderTranslation entry triggers fresh rendering.
//
// Returns:
//   - result: the wire-message stream (the messages array of the
//     native request body). Length ≤ len(msgs); state-only figaro
//     tics that emit no wire output are omitted.
//   - perFLT: parallel-indexed with msgs. perFLT[i] is the wire
//     bytes (without cache_control) of the wire message that
//     msgs[i] produced, or nil for state-only tics that emitted
//     nothing. Used by ProjectionSummary to populate the
//     translation log.
func (a *Anthropic) projectMessages(msgs []message.Message, priorTranslations causal.Slice[message.ProviderTranslation]) (result []nativeMessage, perFLT []json.RawMessage) {
	perFLT = make([]json.RawMessage, len(msgs))
	prevSnap := chalkboard.Snapshot{}

	emit := func(idx int, nm nativeMessage) {
		result = append(result, nm)
		if raw, err := json.Marshal(nm); err == nil {
			perFLT[idx] = raw
		}
	}

	for i, msg := range msgs {
		// Track running snapshot for reminder rendering, even on
		// cache-hit paths where we use the cached translation verbatim.
		//
		// The snapshot is what lets a future reminder-policy switch
		// pick between "render only the keys this tic patched"
		// (today's behavior) and "render the full snapshot every
		// turn." Both modes need prevSnap to stay in step with the
		// timeline; advancing it on cache hits keeps the contract
		// uniform across rendered and cached messages.
		//
		// Today the only template that reads the snapshot indirectly
		// (via Entry.Old) is model.tmpl, which has no public surface,
		// so this is mostly defensive — the contract is what we're
		// preserving, not the current behavior.
		applyPatches := func() {
			for _, p := range msg.Patches {
				prevSnap = prevSnap.Apply(p)
			}
		}

		// Try the cached translation first. The cache hit path expects
		// exactly one wire message per Message for now (the typical
		// case); variadic translation (multiple wire messages per
		// Message) is reserved for future N:1 native:figaro support.
		//
		// The translation log can also hold non-message entries
		// keyed by figaro_lt (today: the system block array tied to
		// the bootstrap flt). json.Unmarshal is lax and would happily
		// decode a systemBlock as an empty-role nativeMessage,
		// shipping bytes the API rejects. Validate the role
		// explicitly before using a cached entry.
		if i < priorTranslations.Len() {
			pt := priorTranslations.At(i)
			if len(pt.Messages) > 0 {
				var cached nativeMessage
				if err := json.Unmarshal(pt.Messages[0], &cached); err == nil &&
					(cached.Role == "user" || cached.Role == "assistant") {
					result = append(result, cached)
					perFLT[i] = pt.Messages[0] // reuse cached bytes verbatim
					applyPatches()
					continue
				}
			}
		}

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
			// Render Patches as system-reminder content blocks on the
			// same user-role wire message. Renderer choice is shared
			// across all messages in the stream.
			blocks = append(blocks, a.renderPatchBlocks(msg.Patches, &prevSnap)...)
			if len(blocks) > 0 {
				emit(i, nativeMessage{Role: "user", Content: blocks})
			}

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
			if len(blocks) > 0 {
				emit(i, nativeMessage{Role: "assistant", Content: blocks})
			}

		case message.RoleSystem:
			// System messages from compacted headers are handled
			// at the block level, not per-message.
			continue
		}
	}
	return result, perFLT
}

// renderPatchBlocks renders the given patches as system-reminder content
// blocks using the configured renderer. Advances prevSnap in step. The
// "tool" renderer is not legal inline (it injects synthetic
// assistant/user pairs which would break alternation here); on that
// setting we fall back to "tag" with a one-time warning per Send.
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

// escapeAttr escapes a string for safe use as an XML-attribute value
// inside a <system-reminder name="…"> tag. We only need the minimal
// XML attribute escaping (quote and ampersand); chalkboard keys are
// expected to be simple identifiers anyway.
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

// --- Encode: IR → request body bytes (pure projection) ---

// Encode produces the API request body for one turn plus the
// per-message projection summary for cache persistence. Pure
// translation; no I/O beyond resolving the auth token (cached).
func (a *Anthropic) Encode(ctx context.Context, msgs []message.Message, snapshot chalkboard.Snapshot, priorTranslations causal.Slice[message.ProviderTranslation], tools []provider.Tool, maxTokens int) ([]byte, provider.ProjectionSummary, error) {
	if maxTokens == 0 {
		maxTokens = a.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}
	a.mu.Lock()
	model := a.Model
	a.mu.Unlock()

	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, provider.ProjectionSummary{}, fmt.Errorf("resolve token: %w", err)
	}
	native, summary := a.projectMessagesWithModel(msgs, snapshot, priorTranslations, tools, maxTokens, isOAuthToken(apiKey), model)
	body, err := json.Marshal(native)
	if err != nil {
		return nil, summary, fmt.Errorf("marshal request: %w", err)
	}
	return body, summary, nil
}

// --- Send: pre-encoded body → HTTP → SSE → assembled native ---

// Send POSTs the pre-encoded body to the Anthropic API, pushes parsed
// native deltas into the bus, and returns the assembled final
// nativeMessage bytes when the stream closes.
func (a *Anthropic) Send(ctx context.Context, body []byte, bus provider.Bus) ([]json.RawMessage, error) {
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve token: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiMessagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
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

	a.mu.Lock()
	model := a.Model
	a.mu.Unlock()

	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}
	assembled := a.consumeSSE(resp.Body, bus, model)
	if len(assembled) == 0 {
		return nil, nil
	}
	return []json.RawMessage{assembled}, nil
}

// sseReadTimeout is how long we wait for the next SSE line before
// treating the connection as stalled.
const sseReadTimeout = 5 * time.Minute

// liveDelta is the figaro-shape live entry the consumer pushes per
// content delta. Subscribers extract the text + ContentType.
type liveDelta struct {
	Delta       string              `json:"delta"`
	ContentType message.ContentType `json:"content_type,omitempty"`
}

// consumeSSE accumulates the assistant turn into a nativeMessage,
// pushes deltas to the Bus as they arrive, and returns the assembled
// final native bytes for the agent to condense the live tail into.
func (a *Anthropic) consumeSSE(body io.ReadCloser, bus provider.Bus, model string) json.RawMessage {
	defer body.Close()

	log("sse: stream started, model=%s", model)

	pushDelta := func(text string, ct message.ContentType) {
		if text == "" {
			return
		}
		raw, _ := json.Marshal(liveDelta{Delta: text, ContentType: ct})
		bus.Push(provider.Event{Payload: []json.RawMessage{raw}})
	}
	var assembled json.RawMessage
	finalize := func(nm nativeMessage) {
		raw, _ := json.Marshal(nm)
		assembled = raw
	}

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

	var (
		nm         nativeMessage
		usage      nativeUsage
		eventType  string
		lines      int
		stopReason string
	)
	nm.Role = "assistant"
	nm.Model = model

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

	emitFinal := func(reason string) {
		if reason != "" {
			stopReason = reason
		}
		if stopReason == "" {
			stopReason = "aborted"
		}
		nm.StopReason = stopReason
		u := usage
		nm.Usage = &u
		finalize(nm)
	}

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
			if stopReason == "" {
				emitFinal("aborted")
			}
			return assembled
		case <-time.After(sseReadTimeout):
			log("sse: read timeout after %d lines (%v with no data)", lines, sseReadTimeout)
			emitFinal("aborted")
			return assembled
		}
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

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
			if json.Unmarshal([]byte(data), &block) != nil {
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
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text,omitempty"`
					Thinking    string `json:"thinking,omitempty"`
					PartialJSON string `json:"partial_json,omitempty"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &delta) != nil || delta.Index >= len(nm.Content) {
				continue
			}
			b := &nm.Content[delta.Index]
			switch delta.Delta.Type {
			case "text_delta":
				b.Text += delta.Delta.Text
				pushDelta(delta.Delta.Text, message.ContentText)
			case "thinking_delta":
				b.Thinking += delta.Delta.Thinking
				pushDelta(delta.Delta.Thinking, message.ContentThinking)
			case "input_json_delta":
				// Buffer partial JSON in a side string; finalized in content_block_stop.
				if s, ok := b.Input.(string); ok {
					b.Input = s + delta.Delta.PartialJSON
				} else {
					b.Input = delta.Delta.PartialJSON
				}
			}

		case "content_block_stop":
			var stop struct{ Index int }
			if json.Unmarshal([]byte(data), &stop) != nil || stop.Index >= len(nm.Content) {
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

		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &md) == nil {
				usage.OutputTokens = md.Usage.OutputTokens
				if md.Delta.StopReason != "" {
					stopReason = md.Delta.StopReason
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
			if json.Unmarshal([]byte(data), &ms) == nil {
				usage.InputTokens = ms.Message.Usage.InputTokens
				usage.CacheRead = ms.Message.Usage.CacheRead
				usage.CacheCreate = ms.Message.Usage.CacheCreate
				log("sse: message_start input_tokens=%d cache_read=%d cache_create=%d",
					usage.InputTokens, usage.CacheRead, usage.CacheCreate)
			}

		case "message_stop":
			log("sse: message_stop output_tokens=%d stop_reason=%s", usage.OutputTokens, stopReason)
			emitFinal("")
			return assembled

		case "error":
			var errEvt struct{ Error struct{ Message string } }
			json.Unmarshal([]byte(data), &errEvt)
			log("sse: received error event: %s", errEvt.Error.Message)
			emitFinal("error")
			return assembled
		}
	}
}
