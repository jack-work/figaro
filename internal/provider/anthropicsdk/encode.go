package anthropicsdk

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// encode projects one IR message to SDK wire bytes. Returns nil if
// the message has no content to send.
func (p *Provider) encode(msg message.Message, prevSnap chalkboard.Snapshot) ([]json.RawMessage, error) {
	snap := prevSnap
	mp, ok := p.renderMessage(msg, &snap)
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(mp)
	if err != nil {
		return nil, fmt.Errorf("marshal MessageParam: %w", err)
	}
	return []json.RawMessage{raw}, nil
}

// renderMessage produces an SDK MessageParam.
func (p *Provider) renderMessage(msg message.Message, prevSnap *chalkboard.Snapshot) (anthropic.MessageParam, bool) {
	switch msg.Role {
	case message.RoleUser:
		var blocks []anthropic.ContentBlockParamUnion
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentText:
				blocks = append(blocks, anthropic.NewTextBlock(c.Text))
			case message.ContentImage:
				blocks = append(blocks, anthropic.NewImageBlockBase64(c.MimeType, c.Data))
			case message.ContentToolResult:
				text := c.Text
				if text == "" {
					text = "(empty)"
				}
				blocks = append(blocks, anthropic.NewToolResultBlock(c.ToolCallID, text, c.IsError))
			}
		}
		blocks = append(blocks, p.renderPatchBlocks(msg.Patches, prevSnap)...)
		if len(blocks) == 0 {
			return anthropic.MessageParam{}, false
		}
		return anthropic.NewUserMessage(blocks...), true

	case message.RoleAssistant:
		var blocks []anthropic.ContentBlockParamUnion
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentText:
				blocks = append(blocks, anthropic.NewTextBlock(c.Text))
			case message.ContentThinking:
				// Signature comes from the model on real turns;
				// re-encoding stored history won't have it.
				blocks = append(blocks, anthropic.NewThinkingBlock("", c.Text))
			case message.ContentToolCall:
				input := toolInput(c.Arguments)
				blocks = append(blocks, anthropic.NewToolUseBlock(c.ToolCallID, input, c.ToolName))
			}
		}
		if len(blocks) == 0 {
			return anthropic.MessageParam{}, false
		}
		return anthropic.NewAssistantMessage(blocks...), true
	}
	return anthropic.MessageParam{}, false
}

// toolInput normalizes zero-argument tool_use to "{}" — the API
// rejects a missing or null input, and the IR drops empty maps
// during a WAL roundtrip.
func toolInput(args map[string]interface{}) interface{} {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	return args
}

// renderPatchBlocks projects chalkboard patches as system-reminder
// text blocks and advances the snapshot.
func (p *Provider) renderPatchBlocks(patches []message.Patch, prevSnap *chalkboard.Snapshot) []anthropic.ContentBlockParamUnion {
	if len(patches) == 0 || p.Templates == nil {
		for _, patch := range patches {
			*prevSnap = prevSnap.Apply(patch)
		}
		return nil
	}
	if p.reminder == "tool" {
		slog.Warn("anthropicsdk: reminder_renderer=tool not supported inline; using tag")
	}
	var out []anthropic.ContentBlockParamUnion
	for _, patch := range patches {
		rendered, err := chalkboard.Render(patch, *prevSnap, p.Templates)
		if err != nil {
			slog.Warn("anthropicsdk: render patch", "err", err)
		} else {
			for _, r := range rendered {
				text := fmt.Sprintf("<system-reminder name=\"%s\">\n%s\n</system-reminder>",
					escapeAttr(r.Key), r.Body)
				out = append(out, anthropic.NewTextBlock(text))
			}
		}
		*prevSnap = prevSnap.Apply(patch)
	}
	return out
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}
