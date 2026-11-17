package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// v0.71.0: `:fork <id>:20` on a figaro held only at its tail kept nothing
// below the cut, seeded a tail read floored at 20, and painted the empty
// landing page over the fork's whole history.
func TestHop_AForkBelowTheHeldTailPaintsItsHistory(t *testing.T) {
	tape := newFrameTape(80, 20)
	in := &interactiveInput{mu: new(renderLock)}
	in.fcli = &emptyAboveReader{top: 19}
	in.lt = newLivelogTurn(tape, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.tr.after = nil
	in.lt.apply(aria.Page{Parts: partsFor(90, 11, "PARENT"), More: aria.More{Before: true}})
	in.lt.tr.buildIndex()

	in.parkSubject("ariaAAAA")
	in.seed = in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 20)
	if n := in.lt.client.Count(); n != 0 {
		t.Fatalf("fixture: the clone kept %d messages below the cut, want none", n)
	}
	if in.seed.kind != seedWindow {
		t.Fatalf("a fork that keeps nothing seeds %+v, want a window of its tail", in.seed)
	}
	in.seedSubject(in.subjectGeneration())
	in.lt.tr.render()

	last := strings.Join(tape.frames[len(tape.frames)-1], "\n")
	if !strings.Contains(last, "FORK-BODY19") {
		t.Fatalf("the fork's last frame does not show its last turn:\n%s", last)
	}
}
