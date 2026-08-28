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
// skills/figaro/contributing/paint-repro.md §8): at 100x40, scrolled to rows 219-240 of 1058, a width
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
// naivePaint is a DIFF painter too, and it carries the identical hole -
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

// vtFromRows paints rows into a fresh screen unconditionally: every row, no
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

// resizeGrid changes the screen's DIMENSIONS. It makes no claim about what a
// terminal does to the surviving cells, because this test never relies on one:
// every cell is overwritten with a marker immediately afterwards (see
// scribbleUnknown). That is deliberate, an earlier version of this test modelled
// "a width change truncates cells in place", which is true of tmux and which the
// companion tmux case confirmed row-for-row, but a test that has to defend a
// model of a terminal is a test carrying a liability. BASILIO's formulation is
// strictly stronger and needs no model at all.
func (v *vtScreen) resizeGrid(w, h int) {
	cells := make([][]vtCell, h)
	for r := 0; r < h; r++ {
		cells[r] = make([]vtCell, w)
		if r < v.h {
			copy(cells[r], v.cells[r])
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

// scribbleUnknown fills every cell with a marker the painter has no record of.
//
// THIS IS THE HEART OF THE TEST, and it is why no terminal behaviour has to be
// modelled. A diffing painter is only sound because t.prev is a true statement
// about the screen. resize() throws that knowledge away, and the contract it
// takes on by doing so is: CONVERGE TO YOUR OWN BELIEF FROM AN ARBITRARY PRIOR
// STATE. Scribbling is the arbitrary prior state, and it is strictly harsher than
// anything a real terminal does: tmux truncates rows on a width change and
// slides them on a height shrink, both of which leave SOME cells right by luck.
// So a painter that converges from a scribble converges from any real resize,
// which makes this an over-approximation in the safe direction: it cannot pass
// while a real terminal fails.
//
// It must only be applied where the painter has genuinely discarded its record.
// Between ordinary frames the painter is ENTITLED to trust t.prev, and scribbling
// there would assert a contract it never made.
func (v *vtScreen) scribbleUnknown() {
	for r := 0; r < v.h; r++ {
		for c := 0; c < v.w; c++ {
			v.cells[r][c] = vtCell{r: '\u00a4'} // ¤ appears in no rendered row
		}
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
// It asserts the invariant after EVERY frame, not just the final one: painter
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
			// resize does what a user's terminal does, in the order it does it: the
			// terminal's grid changes first (it has already happened by the time
			// SIGWINCH is delivered), then the application is told. The scribble
			// stands in for "whatever the terminal left behind", harsher than any
			// real case: see scribbleUnknown.
			resize := func(nw, nh int) {
				vt.resizeGrid(nw, nh)
				vt.scribbleUnknown()
				w, h = nw, nh
				tr.resize(nw, nh)
			}

			step("enter")

			// Scroll off the tail into history, so the viewport is full of
			// message separators, a blank row followed by a rule, per
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

			// WIDTH ONLY: no row moves, so nothing but the paint diff can explain
			// a difference.
			resize(72, h)
			step("after width shrink 100->72")

			resize(120, h)
			step("after width grow 72->120")

			// HEIGHT, BOTH DIRECTIONS. Growth was never judged by the real-pty
			// oracle at all, a taller viewport pulls history, the row total moves
			// (1028+ -> 1212+) and the jog lands on a different viewport, so that
			// oracle correctly refuses to compare. Deterministically it is settled
			// here, and on pristine code it fails in every direction.
			resize(120, 24)
			step("after height shrink 40->24")

			resize(120, 56)
			step("after height grow 24->56")

			resize(64, 20)
			step("after both 120x56 -> 64x20")

			resize(140, 60)
			step("after both grow 64x20 -> 140x60")

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

// ---------------------------------------------------------------------------
// ADVERSARIAL: does the belief invariant survive gestures with NO RESIZE?
//
// ALMAVIVA's convergence claim is that gap contamination and resize duplication
// are one bug, reached only through a resize. His evidence was a real-pty sweep
// showing scroll / C-n / Enter / C-o clean: but on ONE aria at ONE geometry,
// and it never covered a search jump, the ! or ? panels, gg/G, a wheel burst, or
// a history page landing mid-view. The user also said gaps are "TYPICALLY fixed
// upon return", and that word leaves room for a fault the repair gesture does
// not repair.
//
// The jog-and-diff oracle CANNOT settle this. It compares the painter against
// itself: if a gesture leaves damage that the jog does not repair, the suspect
// frame and the "truth" frame are equally wrong, the diff is empty, and it
// reports clean. That blind spot is exactly where a second root cause would
// hide. So this runs in the VT harness instead, where t.prev is absolute ground
// truth and every frame is checked directly: no comparison against another
// frame, no blind spot, no resize anywhere in the test.
//
// If this ever fails, there is a SECOND bug and it is not BASILIO's.
// ---------------------------------------------------------------------------

func TestTranscriptPaint_GesturesKeepBelief(t *testing.T) {
	for _, regions := range []bool{true, false} {
		name := "scroll-regions"
		if !regions {
			name = "full-repaint-fallback"
		}
		t.Run(name, func(t *testing.T) {
			defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
			transcriptScrollRegions = regions

			const w, h = 100, 40
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			committed := make([]aria.TurnPart, 16)
			for i := range committed {
				committed[i] = aria.TurnPart{Turn: aria.Turn{
					ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 16),
				}}
			}
			client.Apply(aria.Page{Parts: committed})

			vt := newVT(w, h)
			tr := newTranscript(vt, w, h, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
			tr.enter()
			// A PASS IS ONLY AS STRONG AS THE NUMBER OF FRAMES IT CHECKED, so the
			// count is reported. If it ever drops sharply, the fixture has stopped
			// exercising something and the negative result has quietly weakened.
			frames := 0
			step := func(what string) {
				t.Helper()
				frames++
				assertBelief(t, vt, tr.prev, w, h, what)
			}
			defer func() { t.Logf("belief invariant held across %d frames, no resize anywhere", frames) }()
			step("enter")

			// gg / G: the deliberate jumps, which do not route through the
			// single-notch gesture and so land wherever they land.
			tr.key('g')
			tr.key('g')
			step("gg to top")
			tr.key('G')
			step("G to tail")

			// A wheel burst: a flick arrives as many reports in ONE read and is
			// coalesced into a single frame. The coalescing is exactly the sort
			// of place a frame gets skipped.
			tr.beginBatch()
			for range 23 {
				tr.scrollBy(-1)
			}
			tr.endBatch()
			step("wheel burst up (batched)")
			tr.beginBatch()
			for range 11 {
				tr.scrollBy(2)
			}
			tr.endBatch()
			step("wheel burst down (batched)")

			// Arrow / page motions through the nav path rather than the letters.
			for _, n := range []navKey{navPageUp, navPageUp, navUp, navPageDown, navDown, navHome, navEnd} {
				tr.navMotion(n)
				step(fmt.Sprintf("nav %d", n))
			}

			// Search: the query paints reverse-video highlights on every match,
			// and n/N jump the viewport an arbitrary distance.
			for range 20 {
				tr.scrollBy(-1)
			}
			step("scroll before search")
			tr.find("transcript")
			step("search find")
			for i := range 6 {
				tr.findRepeat(1)
				step(fmt.Sprintf("search next %d", i))
			}
			for i := range 4 {
				tr.findRepeat(-1)
				step(fmt.Sprintf("search prev %d", i))
			}

			// Panels grow the footer, which shrinks the body: the same
			// row-budget change a resize causes, but WITHOUT the terminal
			// reflowing underneath. A good place for a second bug to live.
			tr.openStatusPit()
			tr.render()
			step("status panel open")
			for i := range 4 {
				tr.scrollBy(-3)
				step(fmt.Sprintf("scroll with status panel %d", i))
			}
			tr.pit.close()
			tr.render()
			step("status panel closed")

			tr.openHelpPit()
			tr.render()
			step("help panel open")
			for i := range 4 {
				tr.scrollBy(3)
				step(fmt.Sprintf("scroll with help panel %d", i))
			}
			tr.pit.close()
			tr.render()
			step("help panel closed")

			// The queued-prompts panel, opened the way the queue itself opens
			// it: which means feeding queuedRows, the only field the panel
			// reads. The call this replaced went to a setter that wrote two
			// fields nobody read, so the panel rendered its "(none)" fallback
			// while the comment claimed otherwise.
			tr.queuedRows = []string{"", "↳ queued messages", "   a queued prompt", "   and another one"}
			tr.showQueuedAuto(true)
			step("queued panel open")
			tr.showQueuedAuto(false)
			step("queued panel closed")

			// Selection + expansion interleaved with scrolling: expansion changes
			// a message's height under the viewport, which moves every row below
			// it without any resize.
			for i := range 6 {
				tr.key(0x0e) // C-n
				step(fmt.Sprintf("select next %d", i))
			}
			tr.key('\r')
			step("expand selected")
			for i := range 4 {
				tr.scrollBy(-2)
				step(fmt.Sprintf("scroll while expanded %d", i))
			}
			tr.key('\r')
			step("collapse selected")

			// Verbosity, which re-renders every retained row.
			tr.key(0x0f) // C-o
			step("verbose on")
			tr.key(0x0f)
			step("verbose off")

			// A long climb into history, which forces page landings mid-view.
			for i := range 80 {
				tr.scrollBy(-1)
				step(fmt.Sprintf("deep climb %d", i))
			}

			// A LIVE STREAMING TURN: the one hunting ground the real-pty sweep
			// could not reach without spending a provider turn, and the one place
			// where content grows under the reader rather than being scrolled
			// over. Driven here by applying an UNSEALED turn repeatedly with more
			// nodes each time, which is what a delta push does.
			//
			// Both postures matter and they take different paths: while FOLLOWING
			// the tail the live region advances the viewport, and while DETACHED
			// the open turn still renders (openMessage is unconditional) beneath
			// a window the reader has scrolled away from.
			tr.key('G') // follow the tail
			step("live: following before stream")
			for n := 1; n <= 10; n++ {
				client.Apply(aria.Page{Parts: []aria.TurnPart{{
					Turn: aria.Turn{ID: 17, Sealed: false, Inquiry: "stream me", Nodes: heavyNodes(17, n)},
				}}})
				tr.render()
				step(fmt.Sprintf("live: streaming while following, %d output lines", n))
			}
			for range 12 { // detach and sit in history
				tr.scrollBy(-1)
			}
			step("live: detached")
			for n := 11; n <= 20; n++ {
				client.Apply(aria.Page{Parts: []aria.TurnPart{{
					Turn: aria.Turn{ID: 17, Sealed: false, Inquiry: "stream me", Nodes: heavyNodes(17, n)},
				}}})
				tr.render()
				step(fmt.Sprintf("live: streaming while detached, %d output lines", n))
			}
			// And the seal, which turns the live region into history.
			client.Apply(aria.Page{Parts: []aria.TurnPart{{
				Turn: aria.Turn{ID: 17, Sealed: true, Inquiry: "stream me", Nodes: heavyNodes(17, 20)},
			}}})
			tr.render()
			step("live: turn sealed")
			tr.key('G')
			step("live: follow after seal")
		})
	}
}
