package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Where E's collapse meets B's painter.
//
// The two axes touch SGR from opposite ends and neither could see the other:
//
//   - E shrinks the row TEXT on the way into the row cache, subtractively. It
//     models the entry rendition as unknown, so a row's leading reset is never
//     dropped, and it preserves the final rendition exactly.
//   - B rewrites the row on the way OUT to the terminal: compacting SGR,
//     addressing from the first differing column, and moving whole runs of rows
//     with DECSTBM + SU/SD. It requires every painted row to start and end in
//     default SGR, because SU/SD blank the rolled-in rows with the *current*
//     background.
//
// E's guarantee is strict (same rendition per cell); B's is looser by licence
// (same appearance: it may drop invisible styling on blank cells). Composed,
// the claim is B's: a viewer cannot tell. This file proves that composition
// rather than assuming it, over real glamour rows, through the real painter,
// on all three paths at once.
//
// ---------------------------------------------------------------------------
// On the two VT models, which the merge leaves side by side.
//
// E recommended unifying them with sgrTerm as the base. Not done, and the
// reason is not effort: they are not two implementations of one thing, they
// are two different EQUIVALENCE RELATIONS, and each axis's proof depends on
// its own being the strict one.
//
//   - sgrTerm (sgr_vt_test.go) compares exact rendition per cell, plus the
//     erase-time background, the cursor and an opaque-escape event log. That
//     strictness is what makes collapseSGR's subtractive argument checkable.
//     Run compactRow's tests through it and they all fail: correctly, because
//     compactRow really does change the rendition of blank cells.
//   - vtScreen (transcript_paint_test.go) compares appearance: a space with no
//     reverse, underline or strike is the same cell whatever its foreground.
//     That is precisely the licence compactRow takes, and a model without it
//     cannot express what B is claiming.
//
// Unifying would mean parameterizing sgrTerm by the relation AND teaching it
// DECSTBM/SU/SD (which only vtScreen has) AND teaching vtScreen ECH/ED and the
// escape log (which only sgrTerm has), a rewrite of the two things that judge
// everything else, for no new coverage. Both are anchored to the same external
// oracle instead: TestTranscriptPaint_RealTerminalReplay replays the pager's
// actual stream through tmux, and on this stack those rows are E-collapsed, so
// the composition is checked against a real terminal and not only two models.
// ---------------------------------------------------------------------------

// sgrPaintFrames builds a scrolling frame sequence out of the real glamour
// corpus: a sliding window over the corpus rows plus a footer that changes
// every frame (which is what drives the shared-prefix suffix update).
func sgrPaintFrames(t testing.TB, w, h, frames int) [][]string {
	t.Helper()
	rows := sgrCorpusRows(t)
	if len(rows) < h+frames {
		t.Fatalf("corpus too small for a %d-frame journey at h=%d: %d rows", frames, h, len(rows))
	}
	out := make([][]string, frames)
	for f := range frames {
		screen := make([]string, h)
		for r := range h - 2 {
			screen[r] = rows[(f+r)%len(rows)]
		}
		screen[h-2] = "\x1b[2m" + strings.Repeat("─", w-16) + fmt.Sprintf(" %d/%d ───", f+1, frames) + "\x1b[0m"
		screen[h-1] = fmt.Sprintf("< figaro · frame %d", f+1)
		out[f] = screen
	}
	return out
}

// paintFrames drives the real painter over a frame sequence and returns the
// terminal it drew on. collapse applies E's transform to every row (as the row
// cache does); regions switches B's scroll-region path on or off.
func paintFrames(t testing.TB, frames [][]string, w, h int, collapse, regions bool) *vtScreen {
	t.Helper()
	saved := transcriptScrollRegions
	transcriptScrollRegions = regions
	defer func() { transcriptScrollRegions = saved }()

	screen := newTeeVT(w, h)
	tr := &transcript{out: screen, w: w, h: h, active: true}
	for _, f := range frames {
		row := make([]string, len(f))
		for i, r := range f {
			if collapse {
				r = collapseSGR(r)
			}
			row[i] = r
		}
		tr.paint(row) // paint takes ownership of the slice
	}
	// Teeth: if the scroll-region path never engaged, the "scrolled" stream is
	// just the flat one and the comparison proves nothing.
	if regions && len(frames) > 4 && !decstbmRE.Match(screen.raw) {
		t.Fatalf("the scroll-region path never engaged over %d frames; this test is vacuous", len(frames))
	}
	return screen.vtScreen
}

// decstbmRE matches a scroll-region set, which is the mechanism under test.
var decstbmRE = regexp.MustCompile(`\x1b\[\d+;\d+r`)

// TestSGRCollapsePaintsIdenticallyThroughTheScrollPainter is the composition
// test. Three ways of putting the same frames on a terminal: the rows as the
// renderers produced them, E's collapsed rows through the full-frame diff, and
// E's collapsed rows through B's scroll-region painter: must be
// indistinguishable to a viewer.
//
// The middle one is the control: if it also failed, the bug would be E's alone.
// Only the third failing would be the composition bug, which is the one neither
// axis could have caught.
func TestSGRCollapsePaintsIdenticallyThroughTheScrollPainter(t *testing.T) {
	const w, h, frames = 100, 24, 60
	seq := sgrPaintFrames(t, w, h, frames)

	plain := paintFrames(t, seq, w, h, false, false)
	flat := paintFrames(t, seq, w, h, true, false)
	scrolled := paintFrames(t, seq, w, h, true, true)

	assertSameGrid(t, plain, flat, "collapsed rows, full-frame diff")
	assertSameGrid(t, plain, scrolled, "collapsed rows, scroll-region painter")
}

// TestSGRCollapsePaintsIdenticallyFrameByFrame is the same claim, but checked
// after EVERY frame rather than at the end. A scroll-region path that drifts
// and self-corrects (a mis-predicted shift repaired by the next full row
// update) would pass the end-state check and fail this one; what the user sees
// is every frame, not the last.
func TestSGRCollapsePaintsIdenticallyFrameByFrame(t *testing.T) {
	const w, h, frames = 100, 24, 40
	seq := sgrPaintFrames(t, w, h, frames)

	for n := 1; n <= frames; n++ {
		prefix := seq[:n]
		plain := paintFrames(t, prefix, w, h, false, false)
		scrolled := paintFrames(t, prefix, w, h, true, true)
		assertSameGrid(t, plain, scrolled, fmt.Sprintf("after frame %d of %d", n, frames))
	}
}

// TestSGRCollapsedRowsKeepThePainterInDefaultSGR is B's invariant, checked
// against E's output with E's stricter model. Two separate things have to hold
// for the scroll region to be safe:
//
//   - compactRow's output for a collapsed row must leave the terminal in the
//     default rendition, so SU/SD blank the rolled-in rows with the default
//     background rather than whatever the last row was tinted with.
//   - a collapsed row must not depend on the rendition the row above left, so
//     the painter may skip the row above entirely.
//
// E already proves the second for the cache; this checks it survives the
// painter, which is the layer that actually decides what gets skipped.
func TestSGRCollapsedRowsKeepThePainterInDefaultSGR(t *testing.T) {
	rows := sgrCorpusRows(t)
	// Rows that end mid-style are the interesting ones, so add some by hand:
	// the corpus is well-behaved, and glamour is not the only producer.
	rows = append(rows,
		"\x1b[41mbackground, never reset",
		"\x1b[4munderline, never reset",
		"\x1b[7mreverse, never reset",
		"\x1b[38;5;252m"+strings.Repeat(" ", 40),
		"\x1b[1;38;5;99mbold colour\x1b[0m\x1b[48;5;238m tinted tail",
	)
	for i, row := range rows {
		painted := string(compactRow(nil, collapseSGR(row)))
		if got := sgrFinalRendition(painted); got != sgrFinalRendition("") {
			t.Fatalf("row %d leaves the terminal in %s, not the default rendition\n row: %q\npaint: %q",
				i, got, row, painted)
		}
	}
}

// TestSGRCollapseNoBackgroundBleedUnderScroll is B's NoBackgroundBleed test
// with E's collapse in front of it, and a scroll on top: the case where an
// unreset row would tint not just the next row's erase but every row the
// scroll region rolls in.
func TestSGRCollapseNoBackgroundBleedUnderScroll(t *testing.T) {
	const w, h = 40, 12
	saved := transcriptScrollRegions
	transcriptScrollRegions = true
	defer func() { transcriptScrollRegions = saved }()

	tinted := "\x1b[48;5;196mred, never reset"
	body := func(off int) []string {
		s := make([]string, h)
		for r := range h {
			if r == h/2 {
				s[r] = collapseSGR(tinted)
				continue
			}
			s[r] = collapseSGR(fmt.Sprintf("\x1b[38;5;252mrow %d of the viewport\x1b[0m", r+off))
		}
		return s
	}

	got := newVT(w, h)
	tr := &transcript{out: got, w: w, h: h, active: true}
	for off := range 8 {
		tr.paint(body(off))
		for r := range h {
			for c := range w {
				cell := got.cells[r][c].appearance()
				if cell.s.bg == "" || r == h/2 {
					continue
				}
				t.Fatalf("offset %d: cell %d,%d has background %q; only row %d sets one",
					off, r, c, cell.s.bg, h/2)
			}
		}
	}
}
