package anthropic

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// applyRenderer dispatches to the configured reminder renderer. The
// renderer mutates req in place (typically by appending content blocks
// to the last user message or by appending synthetic tool messages).
//
// Both renderers are no-ops when reminders is empty.
//
// Critical invariant: renderers must NEVER touch req.System, req.Tools,
// or any message before the leaf user message. The cache prefix
// (everything from the start of the request up through the breakpoint
// set by markCacheBreakpoints) must remain byte-stable across turns
// with identical chalkboard state.
func (a *Anthropic) applyRenderer(req *nativeRequest, reminders []chalkboard.RenderedEntry) {
	if len(reminders) == 0 {
		return
	}
	switch a.ReminderRenderer {
	case "tool":
		renderTool(req, reminders)
	case "tag", "":
		renderTag(req, reminders)
	default:
		// Unknown renderer name: warn and fall back to tag.
		log("warning: unknown reminder_renderer %q, using tag", a.ReminderRenderer)
		renderTag(req, reminders)
	}
}

// renderTag wraps each rendered entry in a <system-reminder> block and
// appends it as an additional content block on the last user message.
// If the last message is not user-role (this should not happen in
// practice — the leaf is the prompt the model is responding to), a new
// synthetic user message is appended; this is logged as a likely caller
// bug.
func renderTag(req *nativeRequest, reminders []chalkboard.RenderedEntry) {
	if len(req.Messages) == 0 {
		log("warning: renderTag called with no messages; appending synthetic user turn")
		req.Messages = append(req.Messages, nativeMessage{Role: "user"})
	}
	last := &req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		log("warning: renderTag invoked when last message is %q (expected user); appending synthetic user turn", last.Role)
		req.Messages = append(req.Messages, nativeMessage{Role: "user"})
		last = &req.Messages[len(req.Messages)-1]
	}
	for _, r := range reminders {
		text := fmt.Sprintf("<system-reminder name=\"%s\">\n%s\n</system-reminder>", escapeAttr(r.Key), r.Body)
		last.Content = append(last.Content, nativeBlock{Type: "text", Text: text})
	}
}

// renderTool appends a synthetic assistant tool_use + user tool_result
// pair after the existing message stream. The synthetic tool is NOT
// declared in req.Tools — the model reads the pair as transcript-only
// context (something the harness gathered for it) and cannot call the
// tool again because no schema exists.
//
// All reminders are bundled into a single assistant/user pair (one
// tool_use per reminder, one tool_result per reminder, sharing the
// same synthetic IDs). This keeps the alternation invariant intact:
// ...prior turn → user (current prompt) → assistant (synthetic
// tool_use) → user (synthetic tool_result) → assistant (response).
func renderTool(req *nativeRequest, reminders []chalkboard.RenderedEntry) {
	assistantContent := make([]nativeBlock, 0, len(reminders))
	userContent := make([]nativeBlock, 0, len(reminders))
	for i, r := range reminders {
		id := fmt.Sprintf("harness-notice-%d", i)
		toolName := r.Key
		assistantContent = append(assistantContent, nativeBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  toolName,
			Input: map[string]interface{}{},
		})
		userContent = append(userContent, nativeBlock{
			Type:      "tool_result",
			ToolUseID: id,
			Content:   []nativeBlock{{Type: "text", Text: r.Body}},
		})
	}
	req.Messages = append(req.Messages,
		nativeMessage{Role: "assistant", Content: assistantContent},
		nativeMessage{Role: "user", Content: userContent},
	)
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
