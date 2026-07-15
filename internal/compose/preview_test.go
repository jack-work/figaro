package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

// driveWrite feeds a running "write" tool through the real compose→server→
// client pipeline as its argument JSON grows, mirroring the drain-loop
// cadence (partial arg deltas plus a recompose after each). The final client
// view's tool-node Output is what the user sees live.
func driveWrite(t *testing.T, argFrames []string, resultText string) []livedoc.Node {
	t.Helper()
	srv := aria.NewServer()
	cli := aria.NewClient()
	srv.Subscribe(func(r aria.AriaRead) { cli.Apply(r) })
	srv.Open(1, "assistant")

	preview := func(name string) string {
		if name == "write" {
			return "content"
		}
		return ""
	}

	// The in-flight message: one tool_invoke, no result yet.
	inv := message.Content{Type: message.ContentToolInvoke, ToolCallID: "w1", ToolName: "write"}
	msg := message.Message{Role: message.RoleAssistant, LogicalTime: 1, Content: []message.Content{inv}}

	argPartials := map[string]string{}
	for _, frame := range argFrames {
		argPartials["w1"] = frame
		srv.Update(Nodes([]message.Message{msg}, nil, argPartials, nil, preview))
	}
	if resultText != "" {
		res := message.Message{Role: message.RoleUser, LogicalTime: 2, Content: []message.Content{
			{Type: message.ContentToolResult, ToolCallID: "w1", ToolName: "write", Text: resultText},
		}}
		srv.Update(Nodes([]message.Message{msg, res}, nil, argPartials, nil, preview))
	}
	if v := cli.View().Open; v != nil {
		return v.Nodes
	}
	return nil
}

func findTool(nodes []livedoc.Node) *livedoc.Node {
	for i := range nodes {
		if nodes[i].Type == livedoc.NodeTool {
			return &nodes[i]
		}
	}
	return nil
}

// The write tool's `content` arg streams in as truncated JSON. The node's
// live Output should show the decoded prefix at each step (tail-bounded),
// growing monotonically with the model.
func TestNodes_WriteContentPreview_StreamsLive(t *testing.T) {
	frames := []string{
		`{"path":"/f","content":"`,        // value opened, empty
		`{"path":"/f","content":"line1`,   // partial first line
		`{"path":"/f","content":"line1\n`, // newline seen
		`{"path":"/f","content":"line1\nline2`,
	}
	nodes := driveWrite(t, frames, "")
	n := findTool(nodes)
	if n == nil {
		t.Fatalf("no tool node in %+v", nodes)
	}
	if n.Status != livedoc.StatusRunning {
		t.Fatalf("want running, got %q", n.Status)
	}
	if n.Output != "line1\nline2" {
		t.Errorf("live preview should show streaming content: got %q, want %q", n.Output, "line1\nline2")
	}
}

// Once the tool result lands, its text wins over the arg-stream preview.
func TestNodes_WriteContentPreview_ResultReplacesPreview(t *testing.T) {
	frames := []string{`{"path":"/f","content":"line1\nline2`}
	nodes := driveWrite(t, frames, "Wrote 12 bytes to /f")
	n := findTool(nodes)
	if n == nil {
		t.Fatalf("no tool node in %+v", nodes)
	}
	if n.Status != livedoc.StatusOK {
		t.Fatalf("want ok, got %q", n.Status)
	}
	if n.Output != "Wrote 12 bytes to /f" {
		t.Errorf("execution output must win once available: got %q", n.Output)
	}
}

// Without a PreviewArg lookup, a running tool with only arg-partials shows
// nothing (previous behavior — arg stream is silently dropped).
func TestNodes_NoPreviewArg_NoLeak(t *testing.T) {
	inv := message.Content{Type: message.ContentToolInvoke, ToolCallID: "b1", ToolName: "bash"}
	msg := message.Message{Role: message.RoleAssistant, LogicalTime: 1, Content: []message.Content{inv}}
	nodes := Nodes([]message.Message{msg}, nil, map[string]string{"b1": `{"command":"ls`}, nil, nil)
	n := findTool(nodes)
	if n == nil || n.Output != "" {
		t.Fatalf("no preview-arg fn ⇒ no arg-derived output: %+v", nodes)
	}
}
