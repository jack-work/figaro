package compose

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

// TestEveryNodeIsStamped: a node's time comes from the message it was written
// in, so it survives the daemon that composed it. The live clocks a turn keeps
// are gone once it is read back from the log, and a block with no time on it
// cannot be placed in the conversation.
func TestEveryNodeIsStamped(t *testing.T) {
	const (
		asked  = 1788903160000
		wrote  = 1788903160355
		landed = 1788903162800
	)
	msgs := []message.Message{
		{Role: message.RoleInput, LogicalTime: 1, Timestamp: asked,
			Content: []message.Content{{Type: message.ContentProse, Text: "do a thing"}}},
		{Role: message.RoleOutput, LogicalTime: 2, Timestamp: wrote, Content: []message.Content{
			{Type: message.ContentThinking, Text: "thinking about it"},
			{Type: message.ContentProse, Text: "on it"},
			{Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "bash"},
		}},
		{Role: message.RoleToolResult, LogicalTime: 3, Timestamp: landed, Content: []message.Content{
			{Type: message.ContentToolResult, ToolCallID: "tc_1", Text: "done"},
		}},
	}
	nodes := Nodes(msgs, nil, nil)
	if len(nodes) == 0 {
		t.Fatal("no nodes")
	}
	for i, n := range nodes {
		if n.At == 0 {
			t.Errorf("node %d (%s) carries no time", i, n.Type)
		}
	}
	var tool livedoc.Node
	for _, n := range nodes {
		if n.Type == livedoc.NodeTool {
			tool = n
		}
	}
	if tool.At != wrote {
		t.Errorf("the tool was written at %d, want %d", tool.At, wrote)
	}
	if tool.StartedAt != wrote || tool.FinishedAt != landed {
		t.Errorf("the call ran %d..%d, want %d..%d: the messages bracket it",
			tool.StartedAt, tool.FinishedAt, wrote, landed)
	}
}
