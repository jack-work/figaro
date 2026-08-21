// Package openaichat implements the Provider for OpenAI-compatible Chat
// Completions endpoints: OpenRouter, and local gateways that speak the same
// dialect (coding-router). The Anthropic-family providers live next door;
// this one exists because the wire format genuinely differs, while the route
// and cache machinery are shared through internal/provider.
package openaichat

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

// chatRequest is one Chat Completions call.
type chatRequest struct {
	Model string `json:"model"`
	// Messages are the rows AS THEY LIE ON DISK, spliced verbatim. The
	// translator log holds wire-final messages; decoding each one into a
	// struct and re-encoding it produced bytes that differ only in key
	// order. The system message is ours, constructed per request, and it
	// is marshalled once and prepended -- so a system message can only be
	// at index 0, which is what the marker relies on.
	Messages []json.RawMessage `json:"messages"`
	Tools    []chatTool        `json:"tools,omitempty"`
	Stream   bool              `json:"stream"`
	// StreamOptions asks for a usage block on the final chunk. Without it a
	// streamed response reports no tokens at all, and figaro's context
	// figure silently flatlines.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	MaxTokens     int            `json:"max_completion_tokens,omitempty"`

	// CacheControl is the request-level directive: the endpoint applies the
	// breakpoint to the last cacheable block and advances it itself as the
	// conversation grows. Preferred for a router, which is the only party
	// that knows which model, and therefore which minimum cacheable size
	// and which breakpoint budget: the request resolved to.
	CacheControl *cacheControl `json:"cache_control,omitempty"`

	// SessionID and PromptCacheKey are the same value under the two names
	// the ecosystem understands, so one request pins sticky routing whether
	// the far end reads OpenRouter's field or OpenAI's.
	SessionID      string `json:"session_id,omitempty"`
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// cacheControl is the Anthropic-shaped directive the whole ecosystem has
// standardised on. The type enum has exactly one value; retention is a
// separate ttl field.
type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// chatMessage keeps Content as raw JSON because the dialect accepts two
// shapes, a bare string, or a list of typed parts, and only the second can
// carry a cache marker. Which shape is used is a property of the route, so a
// message's bytes never change between turns.
type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []toolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	ImageURL     *imageURL     `json:"image_url,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Index    *int         `json:"index,omitempty"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// chatUsage is the response usage block.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	Details          struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

// toIR maps gateway usage into figaro's four buckets.
func (u chatUsage) toIR() *message.Usage {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return nil
	}
	input := u.PromptTokens - u.Details.CachedTokens - u.Details.CacheWriteTokens
	if input < 0 {
		input = 0
	}
	return &message.Usage{
		InputTokens:      input,
		OutputTokens:     u.CompletionTokens,
		CacheReadTokens:  u.Details.CachedTokens,
		CacheWriteTokens: u.Details.CacheWriteTokens,
	}
}

// textContent renders one message's content in the shape the route uses.
// blocks=false yields a bare JSON string; blocks=true yields a one-element
// part list. The choice is uniform for the whole aria (it is baked into the
// Fingerprint), so no message ever changes shape between turns.
func textContent(text string, blocks bool) (json.RawMessage, error) {
	if !blocks {
		return json.Marshal(text)
	}
	return json.Marshal([]contentPart{{Type: "text", Text: text}})
}

// stampLeafCache attaches a marker to the last part of a message. It fails
// closed on bare-string content: a string has nowhere to hang a marker, and
// silently rewriting it into a block list mid-conversation would change the
// bytes of an already-cached prefix.
func stampLeafCache(m *chatMessage, cc *cacheControl) bool {
	if m == nil || len(m.Content) == 0 {
		return false
	}
	var parts []contentPart
	if err := json.Unmarshal(m.Content, &parts); err != nil || len(parts) == 0 {
		return false
	}
	parts[len(parts)-1].CacheControl = cc
	raw, err := json.Marshal(parts)
	if err != nil {
		return false
	}
	m.Content = raw
	return true
}

// encodeMessage projects one IR message onto the dialect. Markers are never
// written here: this output is cached per-LT, and a marker baked into a
// cached message would be immovable exactly where it has to move.
func encodeMessage(msg message.Message, blocks bool, reminders []string) ([]chatMessage, error) {
	switch msg.Role {
	case message.RoleInput:
		var text string
		var results []chatMessage
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				text = joinText(text, c.Text)
			case message.ContentToolResult:
				body := c.Text
				if c.IsError && body == "" {
					body = "tool failed"
				}
				content, err := textContent(body, blocks)
				if err != nil {
					return nil, err
				}
				// Tool results are their own role in this dialect, and each
				// one names the call it answers.
				results = append(results, chatMessage{
					Role:       "tool",
					ToolCallID: c.ToolCallID,
					Content:    content,
				})
			case message.ContentImage:
				// Images ride as a part on a user message; a bare-string
				// route cannot carry one, so it is described instead.
				if !blocks {
					text = joinText(text, "[image omitted: this endpoint takes text content only]")
					continue
				}
				parts := []contentPart{{
					Type:     "image_url",
					ImageURL: &imageURL{URL: dataURL(c.MimeType, c.Data)},
				}}
				raw, err := json.Marshal(parts)
				if err != nil {
					return nil, err
				}
				results = append(results, chatMessage{Role: "user", Content: raw})
			}
		}
		for _, r := range reminders {
			text = joinText(text, r)
		}
		out := results
		if text != "" {
			content, err := textContent(text, blocks)
			if err != nil {
				return nil, err
			}
			// The prose turn follows the tool results it responds to.
			out = append(out, chatMessage{Role: "user", Content: content})
		}
		return out, nil

	case message.RoleOutput:
		var text string
		var calls []toolCall
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				text = joinText(text, c.Text)
			case message.ContentToolInvoke:
				args := "{}"
				if c.Arguments != nil {
					b, err := json.Marshal(c.Arguments)
					if err != nil {
						return nil, fmt.Errorf("marshal tool arguments: %w", err)
					}
					args = string(b)
				}
				calls = append(calls, toolCall{
					ID:       c.ToolCallID,
					Type:     "function",
					Function: functionCall{Name: c.ToolName, Arguments: args},
				})
			}
			// Thinking is deliberately dropped: this dialect has no signed
			// thinking block to replay, and an unsigned one is rejected.
		}
		if text == "" && len(calls) == 0 {
			return nil, nil
		}
		m := chatMessage{Role: "assistant", ToolCalls: calls}
		if text != "" {
			content, err := textContent(text, blocks)
			if err != nil {
				return nil, err
			}
			m.Content = content
		}
		return []chatMessage{m}, nil

	case message.RoleSystemInterrupt:
		// Every dangling call needs an answer or the next request is
		// malformed.
		var out []chatMessage
		for _, c := range msg.Content {
			if c.Type != message.ContentInterrupt {
				continue
			}
			body := c.Text
			if body == "" {
				body = "interrupted"
			}
			content, err := textContent(body, blocks)
			if err != nil {
				return nil, err
			}
			out = append(out, chatMessage{Role: "tool", ToolCallID: c.ToolCallID, Content: content})
		}
		return out, nil

	case message.RoleSystem:
		var text string
		for _, c := range msg.Content {
			if c.Type == message.ContentProse {
				text = joinText(text, c.Text)
			}
		}
		if text == "" {
			return nil, nil
		}
		content, err := textContent(text, blocks)
		if err != nil {
			return nil, err
		}
		return []chatMessage{{Role: "system", Content: content}}, nil
	}
	return nil, nil
}

func joinText(acc, add string) string {
	if add == "" {
		return acc
	}
	if acc == "" {
		return add
	}
	return acc + "\n\n" + add
}

func dataURL(mime, data string) string {
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + data
}

// systemMessage builds the leading system turn from the credo. It is rebuilt
// every turn from live form state and never cached per-LT.
func systemMessage(snapshot form.Snapshot, blocks bool) (*chatMessage, error) {
	text := readCredo(snapshot)
	if text == "" {
		return nil, nil
	}
	content, err := textContent(text, blocks)
	if err != nil {
		return nil, err
	}
	return &chatMessage{Role: "system", Content: content}, nil
}

// readCredo extracts the credo, accepting the bare-string and the
// ContentEnvelope shapes the outfitter emits.
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

func projectTools(tools []provider.Tool) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, len(tools))
	for i, t := range tools {
		out[i] = chatTool{
			Type: "function",
			Function: toolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		}
	}
	return out
}

// markRequest applies the resolved plan.
func markRequest(req *chatRequest, policy provider.CachePolicy, plan provider.MarkPlan, caps provider.CacheCaps) int {
	if policy.Off() || !plan.Marking() {
		return 0
	}
	cc := &cacheControl{Type: policy.Type}
	if caps.TTL {
		cc.TTL = policy.TTL
	}
	if plan.TopLevel {
		req.CacheControl = cc
		return 0
	}

	budget := caps.MaxMarkers
	if budget > provider.AutoCacheBreakpoints {
		budget = provider.AutoCacheBreakpoints
	}
	marks := 0
	// THE SYSTEM MESSAGE IS AT INDEX 0 OR NOWHERE: it is constructed here
	// and prepended before any row.
	if budget > 0 && len(req.Messages) > 0 {
		if m := rowChat(req.Messages[0]); m.Role == "system" && stampLeafRow(&req.Messages[0], cc) {
			budget--
			marks++
		}
	}
	if !plan.Tail || budget <= 0 {
		return marks
	}
	if n := len(req.Messages); n > 0 {
		if m := rowChat(req.Messages[n-1]); m.Role != "system" && stampLeafRow(&req.Messages[n-1], cc) {
			marks++
		}
	}
	return marks
}

// rowChat decodes one spliced row. Off the assembly path by design: only
// marking, counting and assertions need a typed view of a single row.
func rowChat(row json.RawMessage) chatMessage {
	var m chatMessage
	_ = json.Unmarshal(row, &m)
	return m
}

// stampLeafRow is stampLeafCache on a spliced row: decode ONE row, stamp
// it, re-encode it. At most two rows per request carry a marker.
func stampLeafRow(row *json.RawMessage, cc *cacheControl) bool {
	m := rowChat(*row)
	if !stampLeafCache(&m, cc) {
		return false
	}
	stamped, err := json.Marshal(m)
	if err != nil {
		return false
	}
	*row = stamped
	return true
}

// countCacheMarkers reports the explicit markers on a request, request-level
// directive included. Five is a hard API error downstream, so this is
// asserted rather than assumed.
func countCacheMarkers(req chatRequest) int {
	n := 0
	if req.CacheControl != nil {
		n++
	}
	for _, row := range req.Messages {
		var parts []contentPart
		if json.Unmarshal(rowChat(row).Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.CacheControl != nil {
				n++
			}
		}
	}
	return n
}
