package compose

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

func TestQuarantinedCallRendersItsBytes(t *testing.T) {
	raw := "{\"edits\": [{\"new_text\": \"\tif len(rows) > 0 {\n\t\tchrome(\"\")"
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "edit",
		Arguments: message.MalformedArgs(raw),
	}
	n := toolNode(inv, 7, 0, 0, nil, nil, nil, nil)

	if n.Args != nil {
		t.Errorf("the envelope must not reach the reader as arguments: %v", n.Args)
	}
	if strings.Contains(n.Summary, message.MalformedArgsKey) {
		t.Errorf("the sentinel key leaked into the summary: %q", n.Summary)
	}
	if n.Input == "" {
		t.Fatal("the bytes that arrived must still be shown")
	}
	if !strings.Contains(n.Input, "if len(rows) > 0 {") {
		t.Errorf("Input is not the payload: %q", n.Input)
	}
}

func TestQuarantinedBytesDoNotDependOnTheLiveMap(t *testing.T) {
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "edit",
		Arguments: message.MalformedArgs("{\"path\": \"x.go\", \"new_text\": \"\tbroken"),
	}
	if n := toolNode(inv, 7, 0, 0, nil, map[string]string{}, map[string]string{}, nil); n.Input == "" {
		t.Error("a reloaded failed call lost the only copy of its arguments")
	}
}

func TestStreamingPrefixStillShows(t *testing.T) {
	inv := message.Content{Type: message.ContentToolInvoke, ToolCallID: "tc_2", ToolName: "write"}
	n := toolNode(inv, 7, 0, 0, nil, map[string]string{}, map[string]string{"tc_2": `{"path": "x.g`}, nil)
	if n.Input != `{"path": "x.g` {
		t.Errorf("Input = %q, want the live prefix", n.Input)
	}
	if n.Status != livedoc.StatusRunning {
		t.Errorf("status = %q", n.Status)
	}
}
