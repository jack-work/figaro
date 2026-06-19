// Package compose maps the Figaro IR (a turn's message.Message blocks)
// to the canonical live-render blob: one markdown string. It is the
// producer-side translation, analogous to a provider Encode — pure,
// deterministic, and dependency-light (no renderer/glamour), so the
// agent can compose without importing the terminal renderer.
//
// Block → markdown mapping (each block is one contiguous span, in order,
// so an edit to one block localizes to a single-region delta):
//   - text      → prose
//   - thinking  → dim blockquote
//   - tool_invoke, still running (no result yet) → "## tool\n⟨spinner⟩ detail"
//   - tool_invoke, completed → "## tool\n✓/✗ detail" + a ```console fence
//     of the result (closed = final; the renderer clamps it to a tail)
//
// A running tool carries the spinner sentinel (animated locally by the
// renderer, off the wire). Tool-result tics (user role) are folded under
// their invoke via tool_call_id; the user's own prompt is a separate
// committed unit and is not part of the agent-turn blob.
package compose

import (
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

const spinner = string(livedoc.SpinnerSentinel)

// Markdown composes the blob for a turn from its messages in order.
func Markdown(msgs []message.Message) string {
	results := indexResults(msgs)

	var b strings.Builder
	for _, m := range msgs {
		if m.Role != message.RoleAssistant {
			continue // tool_result tics fold under their invoke; user prompts aren't in the turn blob
		}
		for _, c := range m.Content {
			switch c.Type {
			case message.ContentText:
				writeBlock(&b, c.Text)
			case message.ContentThinking:
				writeBlock(&b, blockquote(c.Text))
			case message.ContentToolInvoke:
				writeBlock(&b, tool(c, results))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func indexResults(msgs []message.Message) map[string]message.Content {
	out := map[string]message.Content{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID != "" {
				out[c.ToolCallID] = c
			}
		}
	}
	return out
}

// tool renders one tool_invoke block: heading + status line, plus a
// clamped console fence once the result has arrived.
func tool(inv message.Content, results map[string]message.Content) string {
	name := inv.ToolName
	if name == "" {
		name = "tool"
	}
	detail := toolDetail(inv.ToolName, inv.Arguments)

	res, done := results[inv.ToolCallID]
	var sb strings.Builder
	sb.WriteString("## " + name + "\n")
	if !done {
		// Running: animated sentinel, no fence yet (unsealed → renderer
		// keeps it live; no body until output arrives via the producer).
		sb.WriteString(spinner + " " + orDefault(detail, "running"))
		return sb.String()
	}
	glyph := "✓"
	if res.IsError {
		glyph = "✗"
	}
	sb.WriteString(glyph + " " + orDefault(detail, statusWord(res.IsError)) + "\n\n")
	sb.WriteString("```console\n")
	if t := strings.TrimRight(res.Text, "\n"); t != "" {
		sb.WriteString(t + "\n")
	}
	sb.WriteString("```")
	return sb.String()
}

// toolDetail extracts the one-line displayable argument for a tool call.
func toolDetail(name string, args map[string]interface{}) string {
	switch name {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit":
		if path, ok := args["path"].(string); ok {
			return path
		}
	}
	return ""
}

func blockquote(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// writeBlock appends a block as one contiguous span followed by a blank
// line, so blocks stay block-addressable and diffs localize.
func writeBlock(b *strings.Builder, span string) {
	if strings.TrimSpace(span) == "" {
		return
	}
	b.WriteString(strings.TrimRight(span, "\n"))
	b.WriteString("\n\n")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func statusWord(isErr bool) string {
	if isErr {
		return "failed"
	}
	return "done"
}
