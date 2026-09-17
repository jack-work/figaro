package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// The flicker oracle: what the screen looks like BETWEEN the first byte of a
// frame and the last.
//
// Every other paint test here compares the grid AFTER a frame against a naive
// repaint, and every one of them passes while the screen is flashing, because
// the intermediate states are exactly what they do not look at. A frame that
// erases a row and then rewrites the same text is invisible on a terminal that
// honours synchronized output and a visible blink on one that does not.
//
// We cannot decide whether the bracket survives. A trace through tmux showed a
// synchronized-update END at byte 4,686 of a 7,418-byte write with fourteen row
// erases after it: tmux parses ?2026 itself and re-emits its own brackets
// around its own redraw, so OUR bracket is a request, not a guarantee, and
// counting balanced starts and ends proves nothing about what the outer
// terminal received. The property that does not depend on anyone else's
// cooperation is this one:
//
//	NO CELL MAY PASS THROUGH BLANK ON ITS WAY FROM ONE GLYPH TO ANOTHER.
//
// A cell whose content is leaving may be erased; that erase is the final state
// and nobody sees a flash. A cell that is blanked and then rewritten within the
// same frame is a flash, bracket or no bracket.
// ---------------------------------------------------------------------------

// flickerVT is a vtScreen that remembers which cells were blanked mid-frame.
// blanked[r][c] is set when an erase turns a cell that held something into
// one that holds nothing, and cleared when the cell is written again -- so at
// the end of a frame, a set bit over a non-blank cell is a cell that blinked.
type flickerVT struct {
	*vtScreen
	blanked  [][]bool
	was      [][]vtCell // content at the moment the cell was blanked
	flickers []flickerCell
	raw      []byte
}

type flickerCell struct {
	row, col int
	was, now vtCell
}

func newFlickerVT(w, h int) *flickerVT {
	f := &flickerVT{vtScreen: newVT(w, h)}
	f.blanked = make([][]bool, h)
	f.was = make([][]vtCell, h)
	for r := 0; r < h; r++ {
		f.blanked[r] = make([]bool, w)
		f.was[r] = make([]vtCell, w)
	}
	f.vtScreen.watch = f.note
	return f
}

// note is the vtScreen cell hook: before is what the cell holds, after is what
// is about to replace it.
func (f *flickerVT) note(r, c int, before, after vtCell) {
	if r < 0 || r >= len(f.blanked) || c < 0 || c >= len(f.blanked[r]) {
		return
	}
	switch {
	case !vtBlank(before) && vtBlank(after):
		// Content is leaving. Remember it: if this same frame writes the cell
		// again, that was a flash and the report can name the glyph.
		if !f.blanked[r][c] {
			f.blanked[r][c], f.was[r][c] = true, before
		}
	case !vtBlank(after):
		if f.blanked[r][c] {
			f.flickers = append(f.flickers, flickerCell{row: r, col: c, was: f.was[r][c], now: after})
			f.blanked[r][c] = false
		}
	}
}

func (f *flickerVT) Write(p []byte) (int, error) {
	f.raw = append(f.raw, p...)
	return f.vtScreen.Write(p)
}

// beginFrame forgets the previous frame's bookkeeping. Flicker is a property
// WITHIN one frame; a cell erased in frame 3 and written in frame 5 is two
// separate, legitimate screens.
func (f *flickerVT) beginFrame() {
	for r := range f.blanked {
		for c := range f.blanked[r] {
			f.blanked[r][c] = false
		}
	}
	f.flickers = f.flickers[:0]
	f.raw = f.raw[:0]
}

func (f *flickerVT) report(limit int) string {
	var b strings.Builder
	for i, fc := range f.flickers {
		if i == limit {
			fmt.Fprintf(&b, "\n\t... and %d more", len(f.flickers)-limit)
			break
		}
		fmt.Fprintf(&b, "\n\trow %d col %d: %q was blanked, then rewritten as %q",
			fc.row, fc.col, string(runeOf(fc.was)), string(runeOf(fc.now)))
	}
	return b.String()
}

func runeOf(c vtCell) []rune {
	if c.r == 0 {
		return []rune{' '}
	}
	return []rune{c.r}
}

// vtBlank reports whether a cell shows nothing at all: a space on the default
// background, with none of the attributes that draw on emptiness.
func vtBlank(c vtCell) bool {
	a := c.appearance()
	return a.r == ' ' && a.s == vtStyle{}
}

// assertNoFlicker fails with the cells that blinked.
func assertNoFlicker(t *testing.T, f *flickerVT, what string) {
	t.Helper()
	if len(f.flickers) == 0 {
		return
	}
	t.Errorf("%s: %d cell(s) were erased and rewritten inside one frame, which"+
		" is a visible flash on any terminal that does not honour ?2026:%s",
		what, len(f.flickers), f.report(8))
}

// TestFlickerOracleCanSayClean is the control. An oracle that only ever
// reports flashes is indistinguishable from one that is stuck, and the three
// tests below are useless the day it starts reporting one unconditionally. So:
// hand it a frame written the way the painter SHOULD write one (overwrite the
// glyph, erase only past the end) and one written the way it must not, and
// require it to tell them apart.
func TestFlickerOracleCanSayClean(t *testing.T) {
	const w, h = 20, 2
	for _, tc := range []struct {
		name  string
		frame string
		want  int
	}{
		{"overwrite in place", "\x1b[1;1Habd\x1b[1;4H\x1b[K", 0},
		{"erase then rewrite", "\x1b[1;1H\x1b[2Kabd", 3},
		{"erase what truly leaves", "\x1b[1;2H\x1b[K", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := newFlickerVT(w, h)
			_, _ = out.Write([]byte("\x1b[1;1Habc"))
			out.beginFrame()
			_, _ = out.Write([]byte(tc.frame))
			if got := len(out.flickers); got != tc.want {
				t.Fatalf("oracle reported %d flicker(s), want %d:%s", got, tc.want, out.report(8))
			}
		})
	}
}

// TestPaintDoesNotBlankCellsItRewrites is the reproduction. A footer-only
// change every frame, run past the two-second resync boundary, which is the
// reported symptom: a spinner ticking in the footer while the body is still,
// flashing the whole screen on a two-second beat.
func TestPaintDoesNotBlankCellsItRewrites(t *testing.T) {
	const w, h = 80, 24
	out := newFlickerVT(w, h)
	clock := time.Unix(0, 0)
	tr := &transcript{out: out, active: true, h: h, now: func() time.Time { return clock }}

	body := make([]string, h)
	for i := range body {
		body[i] = fmt.Sprintf("\x1b[38;5;252mrow %d holds some prose that does not change\x1b[0m", i)
	}
	frame := func(spin string) []string {
		s := append([]string(nil), body...)
		s[h-1] = "\x1b[2mstatus " + spin + " \u00b7 ctx 1k\x1b[0m"
		return s
	}

	// Frame 0 paints the whole screen from nothing: every cell goes blank ->
	// glyph, which is not a flash.
	tr.paint(frame("|"))

	for i, spin := range []string{"/", "-", "\\", "|", "/", "-"} {
		clock = clock.Add(900 * time.Millisecond) // crosses the 2s resync twice
		out.beginFrame()
		tr.paint(frame(spin))
		assertNoFlicker(t, out, fmt.Sprintf("footer tick %d (spinner %q)", i, spin))
	}
}

// TestPaintDoesNotBlankCellsItRewrites_Scrolling is the same property over the
// ordinary streaming frame: the viewport moves and the content under it
// changes. The scroll-region planner is off, because a row that genuinely
// scrolls in IS blank before it is written and there is no way to avoid that;
// this test is about the rows the diff repaints.
func TestPaintDoesNotBlankCellsItRewrites_Scrolling(t *testing.T) {
	defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
	transcriptScrollRegions = false

	const w, h = 100, 40
	out := newFlickerVT(w, h)
	tr := scrollTranscript(t, out, w, h, 12)
	tr.scrollBy(-40)

	for i := 0; i < 12; i++ {
		out.beginFrame()
		tr.scrollBy(-1)
		assertNoFlicker(t, out, fmt.Sprintf("scroll step %d", i))
	}
}

// TestPaintDoesNotBlankCellsItRewrites_Live drives the real composer over a
// growing open turn: the case the synthetic frames above cannot produce, where
// rows change width, styles and wide glyphs all at once.
func TestPaintDoesNotBlankCellsItRewrites_Live(t *testing.T) {
	const w, h = 100, 30
	out := newFlickerVT(w, h)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, 6)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 12)}}
	}
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
	tr := newTranscript(out, w, h, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
	tr.enter()

	textNodes := func(text string) []livedoc.Node {
		return []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "The answer so far.\n\n" + text},
		}
	}
	grow := []string{
		"a short line",
		"a short line that has grown considerably longer than it was",
		"a short line that has grown considerably longer than it was, \x1b[1mbold\x1b[0m now",
		"\u65e5\u672c\u8a9e wide glyphs arrive",
		"short again",
	}
	for i, text := range grow {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{
			Turn: aria.Turn{ID: 7, Nodes: textNodes(text)},
		}}}, aria.Notify)
		out.beginFrame()
		tr.render()
		assertNoFlicker(t, out, fmt.Sprintf("live growth %d", i))
	}
}
