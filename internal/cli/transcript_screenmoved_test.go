package cli

import (
	"strings"
	"testing"
	"time"
)

// The bug these tests exist for, measured in tmux against a real binary before
// a line of this was written:
//
//	status rows on screen, pager up, turn streaming ......... 1
//	after ONE stray write of "\n"+text to the pane's tty .... 2
//	four seconds later ..................................... 2   (persists)
//	after a terminal resize ................................ 1   (heals)
//
// The second status row is a FROZEN one — spinner stopped mid-animation —
// sitting to the right of ordinary prose, which is the user's report exactly:
//
//	   Read the form tail → get K → append⠴ · ctx ~328.0k/1.0m 32.8% · cost …
//
// Mechanism: the painter finishes every frame on the last row, the alt screen
// has no scrollback, so a write of "\n"+text there SCROLLS THE WHOLE GRID and
// leaves t.prev — "the frame the terminal is holding" — describing a screen
// that no longer exists. Every row that composes identically is then skipped,
// and every row that differs is updated from a shared-prefix divergence column
// (appendRowUpdate), i.e. a TAIL written onto a row whose left half is now
// something else. Nothing repaired it, because nothing invalidated t.prev;
// resize did, which is why resizing "fixed" it by hand.

// strayWrite does to the VT exactly what `fmt.Fprintln(os.Stderr, "\n"+text)`
// does to a real terminal with the cursor parked on the last row: the grid
// scrolls up one, and the text lands on the bottom row. The transcript is NOT
// told — that is the whole point.
func strayWrite(v *vtScreen, text string) {
	v.row, v.col = v.h-1, 0
	v.scroll(1)
	for _, r := range text {
		v.put(r)
	}
}

// TestScreenMovedRepaintsInFull is the regression test proper: after an
// unannounced scroll, a frame whose composition is unchanged except for one
// row must still restore the WHOLE screen once screenMoved is called.
//
// CANARY (run it): delete the tr.screenMoved() line and this test fails with
// the contaminated grid — the stale row survives, which is the bug.
func TestScreenMovedRepaintsInFull(t *testing.T) {
	const w, h = 40, 6
	frame := func(status string) []string {
		return []string{"alpha row", "beta row", "gamma row", "delta row", "───── rule", status}
	}
	screen := newVT(w, h)
	tr := &transcript{out: screen, w: w, h: h, active: true, now: func() time.Time { return time.Time{} }}

	tr.paint(append([]string(nil), frame("status ⠋ · ctx 1k")...))
	if got := screen.text()[0]; !strings.HasPrefix(got, "alpha row") {
		t.Fatalf("setup: row 0 = %q", got)
	}

	// Something outside the frame buffer writes "\n"+text: the grid scrolls.
	strayWrite(screen, "STRAY WRITE")

	// Without being told, the painter cannot know. Prove the hazard is real
	// (pin the bug, so this test cannot pass for the wrong reason).
	tr.paint(append([]string(nil), frame("status ⠙ · ctx 1k")...))
	if got := screen.text()[0]; strings.HasPrefix(got, "alpha row") {
		t.Fatal("hazard gone: the painter repaired an unannounced scroll by itself — " +
			"if that is now true, this test is measuring nothing and must be rewritten")
	}

	// Told, it repairs everything on the next frame.
	tr.screenMoved()
	tr.paint(append([]string(nil), frame("status ⠹ · ctx 1k")...))
	want := frame("status ⠹ · ctx 1k")
	for r, w := range want {
		if got := screen.text()[r]; got != w {
			t.Errorf("row %d after screenMoved: got %q, want %q", r, got, w)
		}
	}
}

// TestResyncRepaintsInFullOnInterval is the bound for writers that never call
// screenMoved (a library, the Go runtime, a provider SDK). The painter must
// re-earn its model within transcriptResyncInterval of ACTIVE painting.
func TestResyncRepaintsInFullOnInterval(t *testing.T) {
	const w, h = 40, 6
	frame := func(status string) []string {
		return []string{"alpha row", "beta row", "gamma row", "delta row", "───── rule", status}
	}
	screen := newVT(w, h)
	now := time.Unix(1000, 0)
	tr := &transcript{out: screen, w: w, h: h, active: true, now: func() time.Time { return now }}

	tr.paint(append([]string(nil), frame("s0")...))
	strayWrite(screen, "STRAY WRITE")

	// Inside the interval: still diffing, so the damage stands.
	now = now.Add(transcriptResyncInterval / 2)
	tr.paint(append([]string(nil), frame("s1")...))
	if got := screen.text()[0]; got == "alpha row" {
		t.Fatalf("resynced too early: row 0 = %q", got)
	}

	// Past it: one unconditional frame, and the screen is whole again.
	now = now.Add(transcriptResyncInterval)
	tr.paint(append([]string(nil), frame("s2")...))
	for r, w := range frame("s2") {
		if got := screen.text()[r]; got != w {
			t.Errorf("row %d after resync: got %q, want %q", r, got, w)
		}
	}
}

// TestResyncDoesNotFireOnEveryFrame guards the cost: the resync is a bound,
// not a policy of repainting everything always. Between intervals the painter
// must still be diffing (one row changed => one row written).
func TestResyncDoesNotFireOnEveryFrame(t *testing.T) {
	const h = 6
	rows := []string{"a", "b", "c", "d", "e", "f"}
	now := time.Unix(1000, 0)
	out := &countingWriter{}
	tr := &transcript{out: out, w: 40, h: h, active: true, now: func() time.Time { return now }}

	tr.paint(append([]string(nil), rows...)) // first frame is full by definition
	first := out.bytes.Load()
	out.bytes.Store(0)

	changed := append([]string(nil), rows...)
	changed[5] = "F"
	now = now.Add(transcriptFrameInterval)
	tr.paint(changed)
	second := out.bytes.Load()
	if second == 0 {
		t.Fatal("second frame wrote nothing")
	}
	if second >= first {
		t.Errorf("second frame wrote %d bytes, as much as the full first frame (%d) — "+
			"the diff is not being used between resyncs", second, first)
	}
}
