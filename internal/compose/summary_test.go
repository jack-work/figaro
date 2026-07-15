package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

func TestNodes_Summary_FromFn(t *testing.T) {
	sum := func(name string, args map[string]any) string {
		if name == "bash" {
			c, _ := args["command"].(string)
			return "$ " + c
		}
		return ""
	}
	nodes := Nodes([]message.Message{assistant(invoke("t1", "bash", "ls -la"))}, nil, sum)
	if len(nodes) != 1 || nodes[0].Type != livedoc.NodeTool {
		t.Fatalf("want 1 tool node: %+v", nodes)
	}
	if nodes[0].Summary != "$ ls -la" {
		t.Errorf("summary from fn: got %q, want %q", nodes[0].Summary, "$ ls -la")
	}
}

func TestNodes_Summary_GenericFallback_NilFn(t *testing.T) {
	// nil ToolSummary → generic sorted key=value pairs.
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "t2", ToolName: "unknown",
		Arguments: map[string]any{"b": 2, "a": "x"},
	}
	nodes := Nodes([]message.Message{assistant(inv)}, nil, nil)
	if got := nodes[0].Summary; got != "a=x b=2" {
		t.Errorf("generic fallback: got %q, want %q", got, "a=x b=2")
	}
}

func TestNodes_Summary_GenericFallback_EmptyReturn(t *testing.T) {
	// A summarizer that returns "" also falls back to key=value.
	sum := func(string, map[string]any) string { return "" }
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "t3", ToolName: "x",
		Arguments: map[string]any{"k": "v"},
	}
	nodes := Nodes([]message.Message{assistant(inv)}, nil, sum)
	if got := nodes[0].Summary; got != "k=v" {
		t.Errorf("empty-return fallback: got %q", got)
	}
}
