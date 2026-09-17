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
	case f.vtScreen.scrolling:
		// A scroll-region shift. The blank row that rolls in is the content
		// moving, not a repaint erasing itself, and there is no way to emit a
		// scroll without it. Clear any mark so the row update that fills the
		// row afterwards is not charged for it.
		f.blanked[r][c] = false
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
	tr := &transcript{out: out, active: true, w: w, h: h, now: func() time.Time { return clock }}

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

// ---------------------------------------------------------------------------
// The tail erase, case by case.
//
// appendRowTailErase is four lines and every one of them is a trap someone
// already fell into. These drive it directly rather than through a transcript,
// because the interesting inputs (a row that exactly fills the line, a row
// ending in a double-width glyph, a row whose padding compacted away) are
// awkward to arrange from the composer and trivial to state here.
// ---------------------------------------------------------------------------

func TestRowTailErase(t *testing.T) {
	const w = 20
	for _, tc := range []struct {
		name          string
		old, row      string
		wantErase     bool
		wantAddressed bool // an erase preceded by its own cursor address
	}{
		{
			name: "shrinking row clears the tail",
			old:  "a longer line", row: "short",
			wantErase: true,
		},
		{
			// Not free, and deliberately so: three bytes to guarantee that
			// nothing of the old row can survive to the right of the new one.
			// The alternative is measuring the old row's emitted width on every
			// changed row, which cost +80% on a half-page jump to save 4% of
			// the bytes, and can under-erase if the measurement is ever wrong.
			name: "growing row still clears behind itself",
			old:  "short", row: "a longer line",
			wantErase: true,
		},
		{
			name: "identical row still clears behind itself",
			old:  "unchanged row", row: "unchanged row",
			wantErase: true,
		},
		{
			// THE RIGHT MARGIN. Autowrap is off, so after writing column 20 the
			// cursor sits ON column 20; an erase from there would delete the
			// character just written. There is nothing beyond the margin, so
			// there must be no erase at all.
			name: "row that fills the line never erases",
			old:  strings.Repeat("o", w), row: strings.Repeat("n", w),
			wantErase: false,
		},
		{
			// The same, reached by a wide glyph rather than by counting: two
			// columns landing exactly on the margin.
			// Eighteen bytes of 'x' plus a three-byte glyph is 21 bytes, over
			// the bound, so the exact count runs and reports 20: the margin.
			name: "wide glyph ending on the margin never erases",
			old:  strings.Repeat("o", w), row: strings.Repeat("x", w-2) + "\u65e5",
			wantErase: false,
		},
		{
			// Emitted BYTES are the cheap upper bound on emitted COLUMNS, and
			// three wide glyphs are nine bytes for six columns. Nine is short
			// of the margin, so the bound answers and no exact count is needed.
			name: "wide glyphs clear from where the cursor stopped",
			old:  strings.Repeat("o", w), row: "\u65e5\u672c\u8a9e",
			wantErase: true, wantAddressed: false,
		},
		{
			// Ninety padding cells wrapped in SGR are hundreds of source bytes
			// for two columns. Compaction throws all of it away, so the EMITTED
			// bytes are "hi" and the cheap bound answers: a count over the
			// source row would have said 92 and put the cursor past the margin.
			name: "trimmed padding is measured after compaction",
			old:  strings.Repeat("o", w), row: "hi" + strings.Repeat("\x1b[38;5;252m \x1b[0m", 90),
			wantErase: true, wantAddressed: false,
		},
		{
			// The case the byte bound CANNOT answer, and the reason the exact
			// count exists: styling that survives compaction. Ten one-character
			// segments in ten different colours are ten columns and about sixty
			// bytes, so the bound says "might have reached the margin" and is
			// wrong. The exact count over the emitted bytes says ten.
			name: "heavily styled short row falls back to an exact count",
			old:  strings.Repeat("o", w),
			row: strings.Join([]string{
				"\x1b[31ma", "\x1b[32mb", "\x1b[33mc", "\x1b[34md", "\x1b[35me",
				"\x1b[36mf", "\x1b[91mg", "\x1b[92mh", "\x1b[93mi", "\x1b[94mj\x1b[0m",
			}, ""),
			wantErase: true, wantAddressed: true,
		},
		{
			// Nothing is written, so the row address and the erase address are
			// the same cursor move, and "addressed" is vacuously true.
			name: "a row that empties clears from column one",
			old:  "was here", row: "",
			wantErase: true, wantAddressed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(appendRowUpdate(nil, 0, w, tc.old, tc.row))
			if n := strings.Count(got, "\x1b[K") + strings.Count(got, "\x1b[2K"); n > 1 {
				t.Fatalf("emitted %d erases; a row update emits at most one: %q", n, got)
			}
			erase := strings.Index(got, "\x1b[K")
			if (erase >= 0) != tc.wantErase {
				t.Fatalf("erase=%v, want %v: %q", erase >= 0, tc.wantErase, got)
			}
			if strings.Contains(got, "\x1b[2K") {
				t.Fatalf("cleared from column one, which blanks text about to be"+
					" rewritten: %q", got)
			}
			if erase < 0 {
				return
			}
			if !strings.HasSuffix(got, "\x1b[K") {
				t.Fatalf("the erase is not the last thing the row does: %q", got)
			}
			// An addressed erase carries its own cursor move; a bare one relies
			// on the terminal having left the cursor at the end of the text.
			if addressed := strings.HasSuffix(got, "H\x1b[K"); addressed != tc.wantAddressed {
				t.Fatalf("addressed=%v, want %v: %q", addressed, tc.wantAddressed, got)
			}
			// Whatever the route, the erase must land where the text stopped.
			vt := newVT(w, 1)
			_, _ = vt.Write([]byte("\x1b[1;1H" + strings.Repeat("\u00a4", w)))
			_, _ = vt.Write([]byte(got))
			if strings.Contains(vt.text()[0], "\u00a4") {
				t.Fatalf("stale content survived the row update: %q", vt.text()[0])
			}
		})
	}
}

// TestRowTailErase_LastColumnSurvives is the trap stated as a fact about the
// screen rather than about the bytes. A row whose text reaches the final column
// must still have that column on screen afterwards. The bug this guards against
// is subtle enough that the byte-level test above could be satisfied by an
// erase emitted in the right place with the cursor in the wrong one.
func TestRowTailErase_LastColumnSurvives(t *testing.T) {
	for _, w := range []int{4, 20, 80} {
		for _, tail := range []string{"Z", "\u65e5", "\x1b[1mZ\x1b[0m"} {
			row := strings.Repeat("a", w-displayWidth(tail)) + tail
			vt := newVT(w, 1)
			_, _ = vt.Write(appendRowUpdate(nil, 0, w, strings.Repeat("o", w), row))
			got := vt.text()[0]
			if displayWidth(got) != w {
				t.Fatalf("w=%d tail=%q: screen holds %d columns, want %d (%q)",
					w, tail, displayWidth(got), w, got)
			}
		}
	}
}

// TestResyncRecoversWithoutBlanking is the fact, and the first version of this
// test asserted a wish instead, so the difference is worth stating.
//
// The wish was that a resync of unchanged rows emit no erases at all: measure
// the stale columns from t.prev, see that nothing shrank, emit nothing. That
// would have quietly cost the safety net its reason to exist. A resync is not a
// repaint of rows we believe changed; it is the recovery for a desync NOBODY
// DETECTED, and the desync it cannot otherwise see is a foreign write that made
// a row WIDER than the painter believes. Trusting t.prev for the width during
// the very frame whose job is to stop trusting t.prev gives up exactly that.
//
// So a resync still clears every row to the right margin. What changed is WHEN:
// the erase follows the text instead of preceding it, which costs one cursor
// address per row (eight bytes) and no longer flashes. That is the whole trade,
// and it is why the fix did not have to touch the cadence.
func TestResyncRecoversWithoutBlanking(t *testing.T) {
	const w, h = 80, 24
	out := newTeeVT(w, h)
	clock := time.Unix(0, 0)
	tr := &transcript{out: out, active: true, w: w, h: h, now: func() time.Time { return clock }}

	rows := make([]string, h)
	for i := range rows {
		rows[i] = fmt.Sprintf("\x1b[38;5;252mrow %d\x1b[0m", i)
	}
	frame := func(last string) []string {
		s := append([]string(nil), rows...)
		s[h-1] = last
		return s
	}
	tr.paint(frame("status |"))

	clock = clock.Add(3 * time.Second) // past the resync interval
	out.reset()
	tr.paint(frame("status /"))
	got := out.lastFrame()

	if !strings.Contains(got, "status /") {
		t.Fatalf("the frame did not repaint the footer: %q", got)
	}
	// Every row is re-earned, so every row clears its own tail.
	if n := strings.Count(got, "\x1b[K"); n != h {
		t.Fatalf("a resync cleared %d row tails, want %d (one per row): %q", n, h, got)
	}
	// And the clearing is the LAST thing each row does. This is the property
	// that stopped the flashing; the count above would be satisfied just as
	// well by the erase-first painter that caused it.
	if strings.Contains(got, "\x1b[2K") {
		t.Fatalf("a row was cleared from column one, which blanks text that is"+
			" about to be rewritten: %q", got)
	}
	for r := 1; r <= h; r++ {
		cup := fmt.Sprintf("\x1b[%d;1H", r)
		i := strings.Index(got, cup)
		if i < 0 {
			t.Fatalf("row %d was not repainted by a resync: %q", r, got)
		}
		rest := got[i+len(cup):]
		if end := strings.Index(rest, "\x1b["+itoaTest(r+1)+";1H"); end >= 0 {
			rest = rest[:end]
		}
		erase := strings.Index(rest, "\x1b[K")
		if erase < 0 {
			continue // a row whose text reaches the margin has no tail
		}
		if text := strings.TrimSpace(visibleText(rest[:erase])); text == "" {
			t.Fatalf("row %d erased before it wrote anything: %q", r, rest)
		}
	}
}

// TestOrdinaryFrameErasesOnlyWhatChanged pins the byte economy the other way
// round: between resyncs, a footer tick must not touch the body at all.
func TestOrdinaryFrameErasesOnlyWhatChanged(t *testing.T) {
	const w, h = 80, 24
	out := newTeeVT(w, h)
	clock := time.Unix(0, 0)
	tr := &transcript{out: out, active: true, w: w, h: h, now: func() time.Time { return clock }}

	rows := make([]string, h)
	for i := range rows {
		rows[i] = fmt.Sprintf("\x1b[38;5;252mrow %d\x1b[0m", i)
	}
	frame := func(last string) []string {
		s := append([]string(nil), rows...)
		s[h-1] = last
		return s
	}
	tr.paint(frame("status |"))
	clock = clock.Add(100 * time.Millisecond) // well inside the resync window
	out.reset()
	tr.paint(frame("status /"))

	got := out.lastFrame()
	if strings.Contains(got, "row 0") {
		t.Fatalf("a footer tick repainted the body: %q", got)
	}
	// One row changed, so exactly one row is addressed and exactly one tail is
	// cleared. The whole frame is under thirty bytes.
	if n := strings.Count(got, "\x1b[K"); n != 1 {
		t.Fatalf("a footer tick emitted %d erase(s), want 1 (its own row): %q", n, got)
	}
	if len(got) > 40 {
		t.Fatalf("a footer tick cost %d bytes, want a handful: %q", len(got), got)
	}
}
