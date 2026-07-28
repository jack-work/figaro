package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// RESIZE MUST LEAVE THE SCREEN EQUAL TO THE PAINTER'S BELIEF.
//
// The bug this file exists to catch, in the user's words:
//
//	"The gaps in between nodes are populated with text that shouldn't be there
//	 from some other line. It can only be cleared by moving the viewport such
//	 that the corrupt region is no longer visible, and then moving back to that
//	 area. It will typically be fixed upon return."
//
// Reproduced in a real tmux pane before this test was written (see
// PAINT-REPRO.md §8): at 100x40, scrolled to rows 219–240 of 1058, a width
// change to 72 columns left FIVE rows holding text from other lines, and a jog
// away and back cleared them.
//
// THE INVARIANT ASSERTED HERE IS DELIBERATELY NOT THE FIX. It is the one
// property every candidate fix must have, and the one the repo already believes
// in (transcript_paint_tmux_test.go asserts it against a real terminal):
//
//	AFTER EVERY PAINT, THE TERMINAL'S GRID EQUALS t.prev.
//
// t.prev IS the painter's claim about what the screen shows; the whole diffing
// strategy is founded on it. Nothing below names `base == nil`, a sentinel prev,
// a full-repaint flag or \x1b[2J, so any of those fixes satisfies it and the
// shipped code fails it.
//
// WHY THE EXISTING "MATCHES NAIVE REPAINT" TEST CANNOT CATCH THIS, which is the
// reason this file is separate rather than another step() in that one:
// naivePaint is a DIFF painter too, and it carries the identical hole —
//
//	var old string
//	if r < len(prev) { old = prev[r] }
//	if screen[r] != old { …emit… }
//
// so with a nil/short prev it also skips every row whose content is the empty
// string. Comparing the optimized painter against it compares two implementations
// of the same mistake and they agree perfectly. The reference here is instead an
// UNCONDITIONAL repaint of every row (vtFromRows), which cannot skip anything.
// A reference oracle that shares the bug is exactly how eight green tests once
// certified broken code.
// ---------------------------------------------------------------------------

// vtFromRows paints rows into a fresh screen unconditionally — every row, no
// diff, no skipping. This is the ground truth for "what t.prev claims the
// terminal shows".
func vtFromRows(w, h int, rows []string) *vtScreen {
	v := newVT(w, h)
	var b strings.Builder
	for r := 0; r < h; r++ {
		row := ""
		if r < len(rows) {
			row = rows[r]
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, row)
	}
	_, _ = v.Write([]byte(b.String()))
	return v
}

// resizeGrid models what a terminal does to its own grid on a WIDTH change: the
// cells stay exactly where they are, truncated at the new width, or padded with
// blanks when it grows. Nothing moves vertically.
//
// That is the whole model, and it is deliberately minimal. It is justified by
// measurement, not by taste: a width-only change in a real tmux pane (100→72,
// then 72→120) contaminated 5 and 3 rows respectively with no height change
// available to explain it (PAINT-REPRO.md §8.1). So a width change alone is a
// sufficient perturbation, and it is the one that isolates the paint diff from
// terminal reflow.
//
// DELIBERATELY NOT MODELLED: the vertical shift a HEIGHT shrink causes. Measured
// on this machine, an alt-screen shrink deletes min(rows_lost, cursor_y) rows
// from the TOP and slides the rest up, because the painter parks the cursor on
// the status row (PAINT-REPRO.md §6). That is a strictly harsher perturbation
// than this one. Modelling it here would buy nothing this test does not already
// prove and would put a guess about tmux's internals into an assertion; the real
// thing is covered by the tmux replay case, where tmux does the resizing itself.
func (v *vtScreen) resizeGrid(w, h int) {
	cells := make([][]vtCell, h)
	for r := 0; r < h; r++ {
		cells[r] = make([]vtCell, w)
		if r < v.h {
			copy(cells[r], v.cells[r]) // truncates when narrower, zero-pads when wider
		}
	}
	v.cells, v.w, v.h = cells, w, h
	v.top, v.bot = 0, h-1
	if v.row >= h {
		v.row = h - 1
	}
	if v.col >= w {
		v.col = w - 1
	}
}

// assertBelief is the assertion: what the terminal holds must equal what t.prev
// claims it holds.
//
// It reports in TEXT first and only falls back to the cell grid when the
// characters agree but the appearance does not. assertSameGrid dumps every cell
// of a row, which for a 100-column row is ~4 KB of "false" and buries the one
// fact you need; a failure message nobody reads is a test nobody trusts.
func assertBelief(t *testing.T, vt *vtScreen, prev []string, w, h int, what string) {
	t.Helper()
	want := vtFromRows(w, h, prev)
	wt, gt := want.text(), vt.text()
	wg, gg := want.grid(), vt.grid()
	bad := 0
	for r := range wg {
		if wg[r] == gg[r] {
			continue
		}
		bad++
		if bad > 4 {
			continue
		}
		kind := "STALE TEXT"
		if wt[r] == gt[r] {
			kind = "appearance only (same characters)"
		}
		t.Errorf("%s: row %d [%s]\n  t.prev claims: %q\n  screen shows:  %q", what, r, kind, wt[r], gt[r])
	}
	if bad > 0 {
		t.Fatalf("%s: %d of %d rows disagree with t.prev at %dx%d", what, bad, h, w, h)
	}
}

// TestTranscriptPaint_ResizeLeavesNoStaleRows is the regression test for the
// gap-contamination / resize-duplication bug.
//
// It asserts the invariant after EVERY frame, not just the final one — painter
// invariant #5. A transient glitch self-heals on the next op, so a test that
// only looks at the settled screen cannot see the very thing the user is
// complaining about: he sees the bad frame, then his own scroll repairs it.
func TestTranscriptPaint_ResizeLeavesNoStaleRows(t *testing.T) {
	for _, regions := range []bool{true, false} {
		name := "scroll-regions"
		if !regions {
			name = "full-repaint-fallback"
		}
		t.Run(name, func(t *testing.T) {
			defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
			transcriptScrollRegions = regions

			const w0, h0 = 100, 40
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			committed := make([]aria.TurnPart, 14)
			for i := range committed {
				committed[i] = aria.TurnPart{Turn: aria.Turn{
					ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 14),
				}}
			}
			client.Apply(aria.Page{Parts: committed})

			vt := newVT(w0, h0)
			tr := newTranscript(vt, w0, h0, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
			tr.enter()

			w, h := w0, h0
			// step asserts the invariant: what the terminal holds == what t.prev
			// claims it holds. Cell-level, via the appearance grid.
			step := func(what string) {
				t.Helper()
				assertBelief(t, vt, tr.prev, w, h, what)
			}
			// blankRows counts rows the pager intends to be empty. If this is
			// zero the test has stopped exercising its own reason for existing:
			// the bug lives precisely in rows whose new content is "".
			blankRows := func() int {
				n := 0
				for _, row := range tr.prev {
					if visibleText(row) == "" {
						n++
					}
				}
				return n
			}
			// resize does what a user's terminal does, in the order it does it:
			// the terminal's grid changes first (it has already happened by the
			// time SIGWINCH is delivered), then the application is told.
			resize := func(nw, nh int) {
				vt.resizeGrid(nw, nh)
				w, h = nw, nh
				tr.resize(nw, nh)
			}

			step("enter")

			// Scroll off the tail into history, so the viewport is full of
			// message separators — a blank row followed by a rule, per
			// entryLine's `case 0: return ""`.
			for i := range 30 {
				tr.scrollBy(-1)
				step(fmt.Sprintf("scroll up %d", i))
			}
			if got := blankRows(); got == 0 {
				t.Fatalf("fixture no longer exercises its stated reason: no blank rows in the viewport")
			} else {
				t.Logf("viewport holds %d intentionally-blank rows at %dx%d", got, w, h)
			}

			// WIDTH ONLY. No row moves; the terminal merely truncates. This is
			// the minimal perturbation that reproduces the bug in a real pane.
			resize(72, h)
			step("after width shrink 100->72")

			resize(120, h)
			step("after width grow 72->120")

			// Height too, and both at once — a real drag of a window corner.
			resize(120, 24)
			step("after height shrink 40->24")

			resize(64, 20)
			step("after both 120x24 -> 64x20")

			// And the repair gesture the user described: moving away and back
			// must not be what makes the screen correct.
			for i := range 8 {
				tr.scrollBy(-2)
				step(fmt.Sprintf("post-resize scroll up %d", i))
			}
			for i := range 8 {
				tr.scrollBy(2)
				step(fmt.Sprintf("post-resize scroll down %d", i))
			}
		})
	}
}
