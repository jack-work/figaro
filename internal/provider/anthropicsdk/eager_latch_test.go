package anthropicsdk

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/message"
)

// THE OPT-IN IS HONOURED UNTIL IT IS DISPROVED.
//
// `system.eager_tool_streaming` turns off the API's per-parameter buffering,
// which is the thing that guarantees a tool argument arrives as complete,
// escaped JSON. Without it the model's own escaping mistakes reach us intact
// and the call cannot be run. That happened five times in one day, only ever
// in arias carrying the key.
//
// So: the user's stated preference stands until the wire proves it costs him a
// tool call, and from then on THIS aria goes back to the buffered path. Not
// every aria, and not by rewriting his chalkboard.
func TestEagerStreamingLatchesOffAfterAQuarantine(t *testing.T) {
	p := &Provider{}
	if !p.eagerAllowed("aria1") {
		t.Fatal("a fresh aria must honour the chalkboard opt-in")
	}

	clean := anthropic.Message{Content: []anthropic.ContentBlockUnion{
		{Type: "tool_use", ID: "t1", Name: "edit", Input: json.RawMessage(`{"path":"x.go"}`)},
	}}
	p.noteQuarantine(context.Background(), "aria1", clean)
	if !p.eagerAllowed("aria1") {
		t.Error("a healthy turn must not disarm anything")
	}

	poisoned := anthropic.Message{Content: []anthropic.ContentBlockUnion{
		{Type: "text", Text: "words"},
		{Type: "tool_use", ID: "t2", Name: "edit",
			Input: mustJSON(t, message.MalformedArgs("{\"new_text\": \"\tbroken"))},
	}}
	p.noteQuarantine(context.Background(), "aria1", poisoned)
	if p.eagerAllowed("aria1") {
		t.Error("after a quarantine this aria must fall back to the buffered path")
	}
	if !p.eagerAllowed("aria2") {
		t.Error("the latch is per aria; a bystander must not lose the feature")
	}

	// The endpoint-level refusal still outranks everything.
	q := &Provider{NoEagerToolStreaming: true}
	if q.eagerAllowed("aria1") {
		t.Error("an endpoint that rejects the field must never be sent it")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
