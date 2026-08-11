package compose

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// A FAILED CALL STILL SHOWS WHAT ARRIVED.
//
// The point of unbuffered tool-input streaming is to watch the bytes land.
// When they turn out not to parse, the thing worth looking at is exactly
// those bytes: so a quarantined call renders as its raw input, not as the
// envelope figaro wrapped it in to keep the wire legal.
func TestQuarantinedCallRendersItsBytes(t *testing.T) {
	raw := "{\"edits\": [{\"new_text\": \"\tif len(rows) > 0 {\n\t\tchrome(\"\")"
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "edit",
		Arguments: message.MalformedArgs(raw),
	}
	n := toolNode(inv, 7, 0, nil, nil, nil, nil)

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

// And it survives a reload: the bytes travel on the message, not in the
// turn's scratch map, so history reads the same as the live view did.
func TestQuarantinedBytesDoNotDependOnTheLiveMap(t *testing.T) {
	inv := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "edit",
		Arguments: message.MalformedArgs("{\"path\": \"x.go\", \"new_text\": \"\tbroken"),
	}
	// argPartials empty: exactly the state after a restart.
	if n := toolNode(inv, 7, 0, nil, map[string]string{}, map[string]string{}, nil); n.Input == "" {
		t.Error("a reloaded failed call lost the only copy of its arguments")
	}
}

// The healthy streaming path is untouched: while arguments are still arriving
// there are no decoded Args, and the live prefix is what there is to show.
func TestStreamingPrefixStillShows(t *testing.T) {
	inv := message.Content{Type: message.ContentToolInvoke, ToolCallID: "tc_2", ToolName: "write"}
	n := toolNode(inv, 7, 0, nil, map[string]string{}, map[string]string{"tc_2": `{"path": "x.g`}, nil)
	if n.Input != `{"path": "x.g` {
		t.Errorf("Input = %q, want the live prefix", n.Input)
	}
	if n.Status != livedoc.StatusRunning {
		t.Errorf("status = %q", n.Status)
	}
}
