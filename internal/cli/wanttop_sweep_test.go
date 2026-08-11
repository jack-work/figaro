package cli

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// wantTop is a STANDING request, so its correctness is not a property of the
// gestures that existed when it was written, it is a property of every gesture
// that will ever move the reader. Two hand-written probes (wanttop_retract_test)
// pin the two holes that were actually there; this pins the RULE, so a gesture
// added later cannot quietly reopen one.
//
// THE INVARIANT: if a keystroke moved the viewport, the reader has asked for
// somewhere other than the beginning, and the standing request must be gone.
//
// The exemption is Home/gg itself, which moves the viewport TO the top and is
// the gesture that arms the request. Nothing else may move it and keep it.
func TestWantTop_AnyKeyThatMovesTheReaderRetractsIt(t *testing.T) {
	// Arming needs history below the window, or the request is satisfied on the
	// spot (pagerTop only arms when !atAriaFloor).
	// wantTopFixture holds turns 5..8 with history below, so gg genuinely
	// arms: a window standing on turn 1 node 0 is already the floor and
	// pagerTop correctly declines to arm anything.
	arm := func() *transcript {
		tr, _ := wantTopFixture(t)
		return tr
	}

	// The gestures that ARE the request; they may move the viewport and keep it.
	exempt := map[string]bool{
		"0x67": true, // 'g': the first half of gg, and the second
		"Home": true,
		"0x1b": true, // ESC: dismisses panels, does not move the reader
	}

	press := func(name string, do func(*transcript)) {
		tr := arm()
		before := tr.offset
		do(tr)
		if tr.offset == before {
			return // did not move the reader; nothing to assert
		}
		if exempt[name] {
			return
		}
		if tr.wantTop {
			t.Errorf("%s moved the viewport (%d -> %d) and left the standing Home armed; "+
				"the next page landing will yank the reader back to line 0",
				name, before, tr.offset)
		}
	}

	for b := 0; b < 128; b++ {
		b := byte(b)
		press(fmt.Sprintf("0x%02x", b), func(tr *transcript) { tr.key(b) })
	}
	for n := navUp; n <= navEnd; n++ {
		n := n
		press(navName(n), func(tr *transcript) { tr.navMotion(n) })
	}
}

// And the other half of the contract: a landing while the request STANDS must
// re-pin to the top, and the floor must clear it, otherwise the walk either
// never arrives or never stops.
func TestWantTop_LandingRepinsAndTheFloorClearsIt(t *testing.T) {
	tr, _ := wantTopFixture(t)
	req, ok := tr.pageCursor()
	if !ok {
		t.Fatal("fixture: a standing Home should arm a backward fetch")
	}
	// A page lands that does NOT reach the floor: hold the top, keep the request.
	tr.applyPage(req, committedPage(aria.Page{
		Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 3, Sealed: true, Inquiry: "older",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "msg03"}},
		}}},
		More: aria.More{Before: true},
	}))
	if tr.offset != 0 {
		t.Errorf("a landing under a standing Home must re-pin to 0, got %d", tr.offset)
	}
	// The floor: an empty read proves it, and the request must clear or the
	// pager keeps asking for history that is not there.
	tr.applyPage(req, historyPage{})
	if tr.wantTop {
		t.Error("reaching the floor must clear the standing Home")
	}
}
