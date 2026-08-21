package anthropicsdk

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

// encode projects one IR message to SDK wire bytes. Returns nil if
// the message has no content to send.
func (p *Provider) encode(msg message.Message, prevSnap form.Snapshot) ([]json.RawMessage, error) {
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
func (p *Provider) renderMessage(msg message.Message, prevSnap *form.Snapshot) (anthropic.MessageParam, bool) {
	switch msg.Role {
	case message.RoleInput:
		toolImages := message.ToolImagesByCall(msg.Content)
		var blocks []anthropic.ContentBlockParamUnion
		lastSender := ""
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				// A user message may be several submissions folded together,
				// each with its own sender. Announce a sender when it CHANGES,
				// so a run of blocks from one caller costs one line and the
				// model still reads an unambiguous "who said what".
				if c.Sender != "" && c.Sender != lastSender {
					blocks = append(blocks, anthropic.NewTextBlock(senderReminder(c.Sender)))
				}
				if c.Sender != "" {
					lastSender = c.Sender
				}
				blocks = append(blocks, anthropic.NewTextBlock(c.Text))
			case message.ContentImage:
				// An image claimed by a tool_result in this same message is
				// rendered inside that block instead. One that names no tool
				// (a user attachment) or whose tool never landed still has to
				// reach the model, so it falls through to a top-level block.
				if _, claimed := toolImages[c.ToolCallID]; claimed && c.ToolCallID != "" {
					continue
				}
				blocks = append(blocks, anthropic.NewImageBlockBase64(c.MimeType, c.Data))
			case message.ContentToolResult:
				text := c.Text
				if text == "" {
					text = "(empty)"
				}
				blocks = append(blocks, toolResultBlock(c.ToolCallID, text, c.IsError, toolImages[c.ToolCallID]))
			}
		}
		// Taken before renderPatchBlocks advances prevSnap: see the note in
		// the anthropic encoder.
		board := *prevSnap
		patchBlocks, advanced := p.renderPatchBlocks(msg.Patches, board)
		*prevSnap = advanced
		blocks = append(blocks, patchBlocks...)
		for _, text := range provider.StudyReminderTexts(msg, board) {
			blocks = append(blocks, anthropic.NewTextBlock(text))
		}
		for _, text := range provider.ForkReminderTexts(msg, board) {
			blocks = append(blocks, anthropic.NewTextBlock(text))
		}
		if len(blocks) == 0 {
			return anthropic.MessageParam{}, false
		}
		return anthropic.NewUserMessage(blocks...), true

	case message.RoleOutput:
		var blocks []anthropic.ContentBlockParamUnion
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentProse:
				blocks = append(blocks, anthropic.NewTextBlock(c.Text))
			case message.ContentThinking:
				// Dropped. This path is the cache-miss fallback only; the
				// signed wire form is cached at production (acc.ToParam).
				// The IR carries no signature, and an unsigned thinking
				// block is a 400 once extended thinking is enabled.
			case message.ContentToolInvoke:
				input := toolInput(c.Arguments)
				blocks = append(blocks, anthropic.NewToolUseBlock(c.ToolCallID, input, c.ToolName))
			}
		}
		if len(blocks) == 0 {
			return anthropic.MessageParam{}, false
		}
		return anthropic.NewAssistantMessage(blocks...), true

	case message.RoleSystemInterrupt:
		// Surrogate: one synthetic user-role tool_result block per
		// dangling tool_use_id, IsError=true. Anthropic requires each
		// tool_use to be followed by a tool_result; this sentinel
		// supplies one so the assistant turn is closeable from the
		// next prompt onward.
		var blocks []anthropic.ContentBlockParamUnion
		for _, c := range msg.Content {
			if c.Type != message.ContentInterrupt || c.ToolCallID == "" {
				continue
			}
			text := c.Text
			if text == "" {
				text = "(tool execution was interrupted)"
			}
			blocks = append(blocks, anthropic.NewToolResultBlock(c.ToolCallID, text, true))
		}
		if len(blocks) == 0 {
			return anthropic.MessageParam{}, false
		}
		return anthropic.NewUserMessage(blocks...), true
	}
	return anthropic.MessageParam{}, false
}

// toolInput normalizes zero-argument tool_use to "{}": the API
// rejects a missing or null input, and the IR drops empty maps
// during a WAL roundtrip.
func toolInput(args map[string]interface{}) interface{} {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	return args
}

// renderPatchBlocks projects form patches as system-reminder
// text blocks and advances the snapshot.
func (p *Provider) renderPatchBlocks(patches []message.Patch, snap form.Snapshot) ([]anthropic.ContentBlockParamUnion, form.Snapshot) {
	if p.Templates != nil && p.reminder == "tool" {
		slog.Warn("anthropicsdk: reminder_renderer=tool not supported inline; using tag")
	}
	var out []anthropic.ContentBlockParamUnion
	snap = form.FoldRender(snap, patches, p.Templates,
		func(r form.RenderedEntry) {
			out = append(out, anthropic.NewTextBlock(fmt.Sprintf(
				"<system-reminder name=\"%s\">\n%s\n</system-reminder>", escapeAttr(r.Key), r.Body)))
		},
		func(err error) { slog.Warn("anthropicsdk: render patch", "err", err) })
	return out, snap
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

// toolResultBlock builds a tool_result whose content is the result text
// followed by any images the tool produced. Anthropic accepts image blocks
// inside a tool_result, so the picture stays attributed to its call rather
// than trailing loose in the turn.
func toolResultBlock(toolUseID, text string, isErr bool, images []message.Content) anthropic.ContentBlockParamUnion {
	if len(images) == 0 {
		return anthropic.NewToolResultBlock(toolUseID, text, isErr)
	}
	content := []anthropic.ToolResultBlockParamContentUnion{
		{OfText: &anthropic.TextBlockParam{Text: text}},
	}
	for _, img := range images {
		content = append(content, anthropic.ToolResultBlockParamContentUnion{
			OfImage: &anthropic.ImageBlockParam{
				Source: anthropic.ImageBlockParamSourceUnion{
					OfBase64: &anthropic.Base64ImageSourceParam{
						Data:      img.Data,
						MediaType: anthropic.Base64ImageSourceMediaType(img.MimeType),
					},
				},
			},
		})
	}
	return anthropic.ContentBlockParamUnion{
		OfToolResult: &anthropic.ToolResultBlockParam{
			ToolUseID: toolUseID,
			IsError:   param.NewOpt(isErr),
			Content:   content,
		},
	}
}

// senderReminder renders one attribution for the model, in the same
// <system-reminder> shape the form uses, so there is one convention for
// "harness metadata inside a message" rather than two.
func senderReminder(sender string) string {
	return "<system-reminder name=\"sender\">" + sender + "</system-reminder>"
}
