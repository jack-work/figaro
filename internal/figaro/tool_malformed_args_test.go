package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// A CALL WHOSE ARGUMENTS NEVER ARRIVED IS REFUSED, NOT GUESSED.
//
// The provider quarantines a tool_use whose input is not JSON (see
// message.MalformedArgs). Two things must follow, and they are the whole of
// the recovery: the tool does not run, and the model is told plainly enough
// that it resends the call on the next round trip.
func TestMalformedCallIsRefusedWithAnActionableError(t *testing.T) {
	bad := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "edit",
		Arguments: message.MalformedArgs(`{"edits": [{"new_text": "` + "\t" + `oops`),
	}
	good := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "tc_2", ToolName: "bash",
		Arguments: map[string]interface{}{"command": "true"},
	}

	// The dispatcher is the one chokepoint every tool passes through.
	if p := newSpecDispatcher(make(chan toolEvent, 1)).dispatch(t.Context(), nil, bad); p != nil {
		t.Fatal("a quarantined call was dispatched: the tool ran on arguments that never arrived")
	}

	tic := (&Agent{}).assembleToolResults(
		[]message.Content{bad, good},
		map[string]bool{"tc_2": true},
		map[string]toolOutcome{"tc_2": {content: []message.Content{{Type: message.ContentProse, Text: "done"}}}},
	)
	if len(tic.Content) != 2 {
		t.Fatalf("tool_result blocks = %d, want 2: the pairing with tool_use must stay intact", len(tic.Content))
	}
	got := tic.Content[0]
	if !got.IsError {
		t.Error("the refused call must come back as an error")
	}
	if got.ToolCallID != "tc_1" {
		t.Errorf("tool_call_id = %q, want tc_1", got.ToolCallID)
	}
	// The shape the API documents: {"INVALID_JSON": "<what arrived>"}, built
	// with the encoder: the payload is by definition full of unescaped quotes
	// and control characters, and this is where they must survive.
	var wrapper map[string]string
	if err := json.Unmarshal([]byte(got.Text), &wrapper); err != nil {
		t.Fatalf("the result content must be the documented JSON wrapper: %v (%q)", err, got.Text)
	}
	if len(wrapper) != 1 {
		t.Errorf("the wrapper carries one key, got %v", wrapper)
	}
	if raw, _ := message.MalformedArgsOf(bad); wrapper["INVALID_JSON"] != raw {
		t.Errorf("the model must get the bytes that arrived, verbatim:\n got %q\nwant %q",
			wrapper["INVALID_JSON"], raw)
	}
	if tic.Content[1].IsError {
		t.Error("the healthy call in the same turn was collateral damage")
	}
}
