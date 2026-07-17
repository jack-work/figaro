package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
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

func TestNodes_TextAndThinking(t *testing.T) {
	nodes := Nodes([]message.Message{assistant(
		message.Content{Type: message.ContentThinking, Text: "let me think"},
		message.Content{Type: message.ContentProse, Text: "Here is the answer."},
	)}, nil, nil, nil, nil)
	if len(nodes) != 2 {
		t.Fatalf("want thinking + text node, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Type != livedoc.NodeThinking || nodes[0].Markdown != "let me think" {
		t.Errorf("thinking should be a thinking node with raw text: %+v", nodes[0])
	}
	if nodes[1].Type != livedoc.NodeProse || nodes[1].Markdown != "Here is the answer." {
		t.Errorf("assistant text node wrong: %+v", nodes[1])
	}
}

func TestNodes_RunningTool(t *testing.T) {
	nodes := Nodes([]message.Message{assistant(invoke("t1", "bash", "ls -la"))}, nil, nil, nil, nil)
	if len(nodes) != 1 || nodes[0].Type != livedoc.NodeTool {
		t.Fatalf("want 1 tool node: %+v", nodes)
	}
	n := nodes[0]
	if n.Name != "bash" || n.Status != livedoc.StatusRunning {
		t.Errorf("running tool node wrong: %+v", n)
	}
	if n.Args["command"] != "ls -la" {
		t.Errorf("args not carried: %+v", n.Args)
	}
	if n.Output != "" {
		t.Errorf("a running tool with no partial should have empty output: %q", n.Output)
	}
}

func TestNodes_RunningToolWithPartial(t *testing.T) {
	partials := map[string]string{"t1": "line1\nline2\n"}
	nodes := Nodes([]message.Message{assistant(invoke("t1", "bash", "tail -f log"))}, partials, nil, nil, nil)
	if nodes[0].Status != livedoc.StatusRunning {
		t.Fatal("tool should still be running")
	}
	if nodes[0].Output != "line1\nline2" {
		t.Errorf("running tool should carry tail-bound partial output: %q", nodes[0].Output)
	}
}

func TestNodes_ToolTiming(t *testing.T) {
	nodes := Nodes(
		[]message.Message{assistant(invoke("t1", "bash", "ls"))},
		nil, nil, nil, nil,
		map[string]ToolTiming{"t1": {StartedAt: 100, FinishedAt: 250}},
	)
	if nodes[0].StartedAt != 100 || nodes[0].FinishedAt != 250 {
		t.Fatalf("tool timing = %+v", nodes[0])
	}
}

func TestNodes_CompletedAndFailedTool(t *testing.T) {
	ok := Nodes([]message.Message{
		assistant(invoke("t1", "bash", "echo hi")),
		toolResultTic(result("t1", "bash", "hi\n", false)),
	}, nil, nil, nil, nil)
	if ok[0].Status != livedoc.StatusOK || ok[0].Output != "hi" {
		t.Errorf("completed tool wrong: %+v", ok[0])
	}
	bad := Nodes([]message.Message{
		assistant(invoke("t2", "bash", "false")),
		toolResultTic(result("t2", "bash", "boom", true)),
	}, nil, nil, nil, nil)
	if bad[0].Status != livedoc.StatusError {
		t.Errorf("failed tool should be error: %+v", bad[0])
	}
}

func TestNodes_SkipsUserPromptAndDeterministic(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{{Type: message.ContentProse, Text: "do the thing"}}},
		assistant(message.Content{Type: message.ContentProse, Text: "on it"}),
	}
	nodes := Nodes(msgs, nil, nil, nil, nil)
	if len(nodes) != 1 || nodes[0].Markdown != "on it" {
		t.Fatalf("the user's prompt must not appear in the agent turn: %+v", nodes)
	}
}
