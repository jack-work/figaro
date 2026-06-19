package compose

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/render"
)

func assistant(blocks ...message.Content) message.Message {
	return message.Message{Role: message.RoleAssistant, Content: blocks}
}
func toolResultTic(c message.Content) message.Message {
	return message.Message{Role: message.RoleUser, Content: []message.Content{c}}
}
func invoke(id, name, cmd string) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name,
		Arguments: map[string]interface{}{"command": cmd}}
}
func result(id, name, text string, isErr bool) message.Content {
	return message.Content{Type: message.ContentToolResult, ToolCallID: id, ToolName: name, Text: text, IsError: isErr}
}

func TestMarkdown_TextAndThinking(t *testing.T) {
	md := Markdown([]message.Message{assistant(
		message.Content{Type: message.ContentThinking, Text: "let me think"},
		message.Content{Type: message.ContentText, Text: "Here is the answer."},
	)})
	if !strings.Contains(md, "> let me think") {
		t.Fatalf("thinking should be a blockquote:\n%s", md)
	}
	if !strings.Contains(md, "Here is the answer.") {
		t.Fatalf("assistant text missing:\n%s", md)
	}
}

func TestMarkdown_RunningTool(t *testing.T) {
	md := Markdown([]message.Message{assistant(invoke("t1", "bash", "ls -la"))})
	if !strings.Contains(md, "## bash") {
		t.Fatalf("tool heading missing:\n%s", md)
	}
	if !strings.Contains(md, string(livedoc.SpinnerSentinel)+" ls -la") {
		t.Fatalf("running tool should carry the spinner sentinel + detail:\n%q", md)
	}
	if strings.Contains(md, "```") {
		t.Fatal("a running tool must not emit a fence yet")
	}
}

func TestMarkdown_CompletedTool(t *testing.T) {
	md := Markdown([]message.Message{
		assistant(invoke("t1", "bash", "echo hi")),
		toolResultTic(result("t1", "bash", "hi\n", false)),
	})
	if !strings.Contains(md, "✓ echo hi") {
		t.Fatalf("completed tool should show ✓ + detail:\n%s", md)
	}
	if !strings.Contains(md, "```console\nhi\n```") {
		t.Fatalf("completed tool should emit a closed console fence:\n%s", md)
	}
	if strings.ContainsRune(md, livedoc.SpinnerSentinel) {
		t.Fatal("completed tool must not carry a spinner sentinel")
	}
}

func TestMarkdown_FailedTool(t *testing.T) {
	md := Markdown([]message.Message{
		assistant(invoke("t1", "bash", "false")),
		toolResultTic(result("t1", "bash", "boom", true)),
	})
	if !strings.Contains(md, "✗ false") {
		t.Fatalf("failed tool should show ✗:\n%s", md)
	}
}

func TestMarkdown_SkipsUserPromptAndDeterministic(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{{Type: message.ContentText, Text: "do the thing"}}},
		assistant(message.Content{Type: message.ContentText, Text: "on it"}),
	}
	md := Markdown(msgs)
	if strings.Contains(md, "do the thing") {
		t.Fatalf("the user's prompt must not appear in the agent turn blob:\n%s", md)
	}
	if Markdown(msgs) != md {
		t.Fatal("Markdown is not deterministic")
	}
}

// TestMarkdown_RendersThroughPipeline ties compose → render: a running
// tool's sentinel animates and a long completed tool output clamps.
func TestMarkdown_RendersThroughPipeline(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 50; i++ {
		big.WriteString("out line\n")
	}
	md := Markdown([]message.Message{
		assistant(invoke("t1", "bash", "seq 50")),
		toolResultTic(result("t1", "bash", big.String(), false)),
		assistant(invoke("t2", "bash", "sleep 9")), // still running
	})

	res := render.Render(md, render.Options{Width: 100, BashCap: 10, Tick: 2})
	out := strings.Join(res.Lines, "\n")
	stripped := strip(out)

	if !strings.Contains(stripped, "last 10 of 50 lines") {
		t.Fatalf("completed tool output should clamp; got:\n%s", stripped)
	}
	if !strings.ContainsRune(stripped, render.SpinnerFrames[2]) {
		t.Fatalf("running tool should animate to frame 2; got:\n%s", stripped)
	}
	if strings.ContainsRune(stripped, livedoc.SpinnerSentinel) {
		t.Fatal("raw sentinel leaked through the renderer")
	}
}

func strip(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
