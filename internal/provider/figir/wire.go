package figir

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

type Tool = provider.Tool

type Request struct {
	Model          string            `json:"model"`
	System         string            `json:"system,omitempty"`
	Messages       []json.RawMessage `json:"messages"`
	Tools          []Tool            `json:"tools,omitempty"`
	Stream         bool              `json:"stream"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	ThinkingEffort string            `json:"thinking_effort,omitempty"`
	ThinkingBudget int               `json:"thinking_budget,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	TopP           *float64          `json:"top_p,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
}

type WireMessage struct {
	Role       string        `json:"role"`
	Content    []WireContent `json:"content"`
	StopReason string        `json:"stop_reason,omitempty"`
}

type WireContent struct {
	Type       string                     `json:"type"`
	Text       string                     `json:"text,omitempty"`
	MimeType   string                     `json:"mime_type,omitempty"`
	Data       string                     `json:"data,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	ToolName   string                     `json:"tool_name,omitempty"`
	Arguments  map[string]interface{}     `json:"arguments,omitempty"`
	IsError    bool                       `json:"is_error,omitempty"`
	Reason     string                     `json:"reason,omitempty"`
	Signature  string                     `json:"signature,omitempty"`
	Meta       map[string]json.RawMessage `json:"meta,omitempty"`
}

func encodeMessage(msg message.Message, reminders []string) (*WireMessage, error) {
	switch msg.Role {
	case message.RoleInput, message.RoleToolResult:
		var blocks []WireContent
		spoke := false
		lastResult := -1
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				if c.Text == "" {
					continue
				}
				spoke = true
				if r := provider.SenderReminder(c.Sender); r != "" {
					blocks = append(blocks, WireContent{Type: "prose", Text: r})
				}
				blocks = append(blocks, WireContent{
					Type:      "prose",
					Text:      c.Text,
					Signature: c.Signature,
					Meta:      c.Meta,
				})
			case message.ContentImage:
				if c.Data == "" {
					continue
				}
				spoke = true
				mime := c.MimeType
				if mime == "" {
					mime = "image/png"
				}
				blocks = append(blocks, WireContent{
					Type:       "image",
					MimeType:   mime,
					Data:       c.Data,
					ToolCallID: c.ToolCallID,
					ToolName:   c.ToolName,
					Text:       c.Text,
					Meta:       c.Meta,
				})
			case message.ContentToolResult:
				blocks = append(blocks, WireContent{
					Type:       "tool_result",
					ToolCallID: c.ToolCallID,
					ToolName:   c.ToolName,
					Text:       c.Text,
					IsError:    c.IsError,
					Meta:       c.Meta,
				})
				lastResult = len(blocks) - 1
			}
		}
		if !spoke && lastResult >= 0 && len(reminders) > 0 {
			body := blocks[lastResult].Text
			for _, r := range reminders {
				body = joinText(body, r)
			}
			blocks[lastResult].Text = body
			reminders = nil
		}
		for _, r := range reminders {
			if r != "" {
				blocks = append(blocks, WireContent{Type: "prose", Text: r})
			}
		}
		if len(blocks) == 0 {
			return nil, nil
		}
		return &WireMessage{Role: "input", Content: blocks}, nil

	case message.RoleOutput:
		var blocks []WireContent
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentThinking:
				if c.Text == "" && c.Signature == "" && len(c.Meta) == 0 {
					continue
				}
				blocks = append(blocks, WireContent{
					Type:      "thinking",
					Text:      c.Text,
					Signature: c.Signature,
					Meta:      c.Meta,
				})
			case message.ContentProse:
				if c.Text == "" {
					continue
				}
				blocks = append(blocks, WireContent{
					Type:      "prose",
					Text:      c.Text,
					Signature: c.Signature,
					Meta:      c.Meta,
				})
			case message.ContentToolInvoke:
				if c.ToolCallID == "" || c.ToolName == "" {
					continue
				}
				blocks = append(blocks, WireContent{
					Type:       "tool_invoke",
					ToolCallID: c.ToolCallID,
					ToolName:   c.ToolName,
					Arguments:  c.Arguments,
					Signature:  c.Signature,
					Meta:       c.Meta,
				})
			}
		}
		if len(blocks) == 0 {
			return nil, nil
		}
		return &WireMessage{
			Role:       "output",
			Content:    blocks,
			StopReason: string(msg.StopReason),
		}, nil

	case message.RoleSystem:
		var blocks []WireContent
		for _, c := range msg.Content {
			if c.Type == message.ContentProse && c.Text != "" {
				blocks = append(blocks, WireContent{Type: "prose", Text: c.Text, Meta: c.Meta})
			}
		}
		if len(blocks) == 0 {
			return nil, nil
		}
		return &WireMessage{Role: "system", Content: blocks}, nil

	case message.RoleSystemInterrupt:
		var blocks []WireContent
		for _, c := range msg.Content {
			if c.Type != message.ContentInterrupt || c.ToolCallID == "" {
				continue
			}
			blocks = append(blocks, WireContent{
				Type:       "interrupt",
				ToolCallID: c.ToolCallID,
				ToolName:   c.ToolName,
				Reason:     string(c.Reason),
				Text:       c.Text,
				Meta:       c.Meta,
			})
		}
		if len(blocks) == 0 {
			return nil, nil
		}
		return &WireMessage{Role: "system.interrupt", Content: blocks}, nil
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

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return strings.ReplaceAll(s, "<", "&lt;")
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

func applyKnobs(req *Request, snap form.Snapshot) error {
	if raw, ok := snap.Get("system.thinking_effort"); ok && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			req.ThinkingEffort = strings.TrimSpace(s)
		} else {
			var n int
			if json.Unmarshal(raw, &n) == nil {
				req.ThinkingEffort = strconv.Itoa(n)
			}
		}
	}
	if raw, ok := snap.Get("system.thinking_budget"); ok && string(raw) != "null" {
		var b int
		if json.Unmarshal(raw, &b) != nil {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				b, _ = strconv.Atoi(strings.TrimSpace(s))
			}
		}
		if b > 0 {
			req.ThinkingBudget = b
		}
	}
	if temp, set, err := optionalFloat(snap, "system.temperature"); err != nil {
		return err
	} else if set {
		req.Temperature = &temp
	}
	if topP, set, err := optionalFloat(snap, "system.top_p"); err != nil {
		return err
	} else if set {
		req.TopP = &topP
	}
	return nil
}

func optionalFloat(snap form.Snapshot, key string) (float64, bool, error) {
	raw, ok := snap.Get(key)
	if !ok || string(raw) == "null" {
		return 0, false, nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, fmt.Errorf("figir: %s must be a number: %w", key, err)
	}
	return v, true, nil
}

func bodyFuncSeq(req Request, rows provider.RowSeq) provider.BodyFunc {
	req.Messages = nil
	return func(w io.Writer) error {
		return provider.WriteJSONWithRowSeq(w, req, "messages", rows)
	}
}
