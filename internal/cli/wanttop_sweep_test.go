package cli

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// If a keystroke moved the viewport, the standing request must be gone; only
// Home/gg, which arms it, is exempt.
func TestWantTop_AnyKeyThatMovesTheReaderRetractsIt(t *testing.T) {
	// pagerTop only arms when !atAriaFloor, so the fixture needs history below.
	arm := func() *transcript {
		tr, _ := wantTopFixture(t)
		return tr
	}

	exempt := map[string]bool{
		"0x67": true,
		"Home": true,
		"0x1b": true,
	}

	press := func(name string, do func(*transcript)) {
		tr := arm()
		before := tr.offset
		do(tr)
		if tr.offset == before {
			return
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

// A landing while the request stands re-pins to the top; the floor clears it.
func TestWantTop_LandingRepinsAndTheFloorClearsIt(t *testing.T) {
	tr, _ := wantTopFixture(t)
	req, ok := tr.pageCursor()
	if !ok {
		t.Fatal("fixture: a standing Home should arm a backward fetch")
	}
	tr.applyPage(req, (aria.Page{
		Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 3, Sealed: true, Inquiry: "older",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "msg03"}},
		}}},
		More: aria.More{Before: true},
	}))
	if tr.offset != 0 {
		t.Errorf("a landing under a standing Home must re-pin to 0, got %d", tr.offset)
	}
	// An empty read is the floor.
	tr.applyPage(req, aria.Page{})
	if tr.wantTop {
		t.Error("reaching the floor must clear the standing Home")
	}
}
