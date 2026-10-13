package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// collapseSGR shrinks row text into the cache preserving rendition; compactRow
// rewrites it for the terminal preserving appearance, and needs every row to
// start and end in default SGR because SU/SD blank with the current background.
// sgrTerm compares rendition and vtScreen appearance: two relations, kept apart.

// The footer changes every frame to drive the shared-prefix suffix update.
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
		tr.paint(row)
	}
	if regions && len(frames) > 4 && !decstbmRE.Match(screen.raw) {
		t.Fatalf("the scroll-region path never engaged over %d frames; this test is vacuous", len(frames))
	}
	return screen.vtScreen
}

var decstbmRE = regexp.MustCompile(`\x1b\[\d+;\d+r`)

// The full-frame diff over collapsed rows is the control.
func TestSGRCollapsePaintsIdenticallyThroughTheScrollPainter(t *testing.T) {
	const w, h, frames = 100, 24, 60
	seq := sgrPaintFrames(t, w, h, frames)

	plain := paintFrames(t, seq, w, h, false, false)
	flat := paintFrames(t, seq, w, h, true, false)
	scrolled := paintFrames(t, seq, w, h, true, true)

	assertSameGrid(t, plain, flat, "collapsed rows, full-frame diff")
	assertSameGrid(t, plain, scrolled, "collapsed rows, scroll-region painter")
}

// A mis-predicted shift repaired by the next row update passes the end-state
// check and fails this one.
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

// A collapsed row must leave the default rendition and must not depend on the
// row above, which the painter may skip.
func TestSGRCollapsedRowsKeepThePainterInDefaultSGR(t *testing.T) {
	rows := sgrCorpusRows(t)
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

// An unreset row would tint every row the scroll region rolls in.
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
