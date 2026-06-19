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

// composeBashCap bounds how many source lines of tool output the blob
// carries; the renderer further clamps the display. Full output lives in
// the canonical Content IR.
const composeBashCap = 200

// Markdown composes the blob for a turn from its messages in order.
// partials carries streamed output for tools still running (keyed by
// tool_call_id); nil/absent means a tool shows only its spinner.
func Markdown(msgs []message.Message, partials map[string]string) string {
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
				writeBlock(&b, tool(c, results, partials))
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

// tool renders one tool_invoke block: heading + status line + a clamped
// console fence. While running it shows the spinner sentinel and any
// streamed partial output; once sealed it shows ✓/✗ and the final result.
// The fence is always closed — the sentinel in the status line keeps a
// running tool's region live in the renderer, so closing it is safe even
// with later (parallel) tool blocks.
func tool(inv message.Content, results map[string]message.Content, partials map[string]string) string {
	name := inv.ToolName
	if name == "" {
		name = "tool"
	}
	detail := toolDetail(inv.ToolName, inv.Arguments)

	res, done := results[inv.ToolCallID]
	var sb strings.Builder
	sb.WriteString("## " + name + "\n")
	if !done {
		sb.WriteString(spinner + " " + orDefault(detail, "running"))
		if p := partials[inv.ToolCallID]; strings.TrimSpace(p) != "" {
			sb.WriteString("\n\n")
			writeFence(&sb, p)
		}
		return sb.String()
	}
	glyph := "✓"
	if res.IsError {
		glyph = "✗"
	}
	sb.WriteString(glyph + " " + orDefault(detail, statusWord(res.IsError)) + "\n\n")
	writeFence(&sb, res.Text)
	return sb.String()
}

// writeFence writes a closed console fence of text, tail-bounded to
// composeBashCap source lines (full output stays in the IR).
func writeFence(sb *strings.Builder, text string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > composeBashCap {
		lines = lines[len(lines)-composeBashCap:]
	}
	sb.WriteString("```console\n")
	if len(lines) > 0 && !(len(lines) == 1 && lines[0] == "") {
		sb.WriteString(strings.Join(lines, "\n") + "\n")
	}
	sb.WriteString("```")
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
