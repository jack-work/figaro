package figaro

import (
	"encoding/json"
	"testing"
)

// A notice spooled across a restart must still be a notice when it is
// redelivered, or its failure would be told back to the figaro that sent it.
func TestASpooledFailureNoticeStaysANotice(t *testing.T) {
	in := event{typ: eventUserPrompt, text: "turn 3 failed: x", segments: []promptSegment{{sender: "bbbbbbbb22222222bbbbbbbb22222222", text: "turn 3 failed: x", notice: true}}}
	raw, err := json.Marshal(spooled(in))
	if err != nil {
		t.Fatal(err)
	}
	var back spooledPrompt
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if segs := back.event().segments; len(segs) != 1 || !segs[0].notice {
		t.Fatalf("the redelivered notice lost its mark: %+v", segs)
	}
}
