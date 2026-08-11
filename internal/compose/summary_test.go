package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func TestNodes_Summary_IsGenericKeyValues(t *testing.T) {
	// Summary is for SEARCH and the clipboard, never for rendering: the CLI's
	// tool table says which argument speaks for a call. So it is generic:
	// sorted key=value pairs, with no per-tool hook.
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "t2", ToolName: "unknown",
		Arguments: map[string]any{"b": 2, "a": "x"},
	}
	nodes := Nodes([]message.Message{assistant(inv)}, nil, nil)
	if got := nodes[0].Summary; got != "a=x b=2" {
		t.Errorf("generic fallback: got %q, want %q", got, "a=x b=2")
	}
}
