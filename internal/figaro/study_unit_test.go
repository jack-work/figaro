package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
)

// NEWEST WINS AT THE CAP, AND THE LOSS IS STATED: eleven pending deltas
// render as the cap's eight newest plus one overflow notice that counts
// the fallen. Render-only projection means a silently dropped delta was
// never seen by anyone, ever — the notice is the difference between a
// policy and a hole.
func TestDrainStudyRemindersStatesOverflow(t *testing.T) {
	a := &Agent{}
	for i := 0; i < 11; i++ {
		a.studies.pending = append(a.studies.pending, studyDelta{
			formID:  "@r",
			version: uint64(i + 1),
			patch:   form.Patch{},
		})
	}
	blocks := a.drainStudyReminders()
	if len(blocks) != 9 {
		t.Fatalf("blocks = %d, want 9 (8 kept + 1 overflow notice)", len(blocks))
	}
	if !strings.Contains(blocks[0], "study-overflow") || !strings.Contains(blocks[0], "3 older") {
		t.Fatalf("overflow notice missing or miscounted: %s", blocks[0])
	}
	if !strings.Contains(blocks[1], `version="4"`) {
		t.Fatalf("oldest kept should be version 4, got: %s", blocks[1])
	}
	if !strings.Contains(blocks[8], `version="11"`) {
		t.Fatalf("newest kept should be version 11, got: %s", blocks[8])
	}
	if got := a.drainStudyReminders(); got != nil {
		t.Fatalf("second drain not empty: %v", got)
	}
}

// A queued casting call SURVIVES a hangup: DrainUserPrompts drops the
// questions and leaves control events standing, and once dequeued a
// castOp runs synchronously to completion in the loop — a cast is never
// split and never dropped by an interrupt. This pins the inbox half of
// that atomicity; the synchronous half is the absence of any yield in
// serviceCast.
func TestHangupLeavesQueuedCastStanding(t *testing.T) {
	inbox := NewInbox(t.Context())
	inbox.Send(event{typ: eventUserPrompt, text: "doomed question"})
	op := &castOp{roleID: "@r", reply: make(chan castResult, 1)}
	inbox.Send(event{typ: eventCast, cast: op})

	drained := inbox.DrainUserPrompts()
	if len(drained) != 1 || drained[0].text != "doomed question" {
		t.Fatalf("drained = %+v, want just the prompt", drained)
	}
	evt, ok := inbox.Recv()
	if !ok || evt.typ != eventCast || evt.cast != op {
		t.Fatalf("the queued cast did not survive the hangup: %+v ok=%v", evt, ok)
	}
}
