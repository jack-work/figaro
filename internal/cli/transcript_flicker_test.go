package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// No cell may pass through blank on its way from one glyph to another within
// a frame. tmux parses ?2026 and re-emits its own brackets, so the bracket is a
// request, not a guarantee.

// flickerVT marks cells blanked mid-frame; a mark over a non-blank cell at
// frame end is a blink.
type flickerVT struct {
	*vtScreen
	blanked  [][]bool
	was      [][]vtCell
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

func (f *flickerVT) note(r, c int, before, after vtCell) {
	if r < 0 || r >= len(f.blanked) || c < 0 || c >= len(f.blanked[r]) {
		return
	}
	switch {
	case f.vtScreen.scrolling:
		// A row rolling in on a scroll is content moving, not an erase.
		f.blanked[r][c] = false
	case !vtBlank(before) && vtBlank(after):
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

// Flicker is a property within one frame.
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

func vtBlank(c vtCell) bool {
	a := c.appearance()
	return a.r == ' ' && a.s == vtStyle{}
}

func assertNoFlicker(t *testing.T, f *flickerVT, what string) {
	t.Helper()
	if len(f.flickers) == 0 {
		return
	}
	t.Errorf("%s: %d cell(s) were erased and rewritten inside one frame, which"+
		" is a visible flash on any terminal that does not honour ?2026:%s",
		what, len(f.flickers), f.report(8))
}

// The control: the oracle must tell a clean frame from a flashing one.
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

// A footer-only tick across the resync boundary must not flash the body.
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

	tr.paint(frame("|"))

	for i, spin := range []string{"/", "-", "\\", "|", "/", "-"} {
		clock = clock.Add(900 * time.Millisecond)
		out.beginFrame()
		tr.paint(frame(spin))
		assertNoFlicker(t, out, fmt.Sprintf("footer tick %d (spinner %q)", i, spin))
	}
}

// Scroll regions off: a row that genuinely scrolls in is blank before it is
// written; this is about the rows the diff repaints.
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

func TestRowTailErase(t *testing.T) {
	const w = 20
	for _, tc := range []struct {
		name          string
		old, row      string
		wantErase     bool
		wantAddressed bool
	}{
		{
			name: "shrinking row clears the tail",
			old:  "a longer line", row: "short",
			wantErase: true,
		},
		{
			// Three bytes guarantee nothing of the old row survives to
			// the right.
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
			// Autowrap is off, so after column w the cursor sits on
			// column w and an erase there would delete the glyph just
			// written.
			name: "row that fills the line never erases",
			old:  strings.Repeat("o", w), row: strings.Repeat("n", w),
			wantErase: false,
		},
		{
			// Over the byte bound, so the exact count runs and reports
			// the margin.
			name: "wide glyph ending on the margin never erases",
			old:  strings.Repeat("o", w), row: strings.Repeat("x", w-2) + "\u65e5",
			wantErase: false,
		},
		{
			// Emitted bytes bound emitted columns; nine bytes is short
			// of the margin.
			name: "wide glyphs clear from where the cursor stopped",
			old:  strings.Repeat("o", w), row: "\u65e5\u672c\u8a9e",
			wantErase: true, wantAddressed: false,
		},
		{
			// Width is measured after compaction, not over the source row.
			name: "trimmed padding is measured after compaction",
			old:  strings.Repeat("o", w), row: "hi" + strings.Repeat("\x1b[38;5;252m \x1b[0m", 90),
			wantErase: true, wantAddressed: false,
		},
		{
			// Styling survives compaction, so the byte bound cannot answer.
			name: "heavily styled short row falls back to an exact count",
			old:  strings.Repeat("o", w),
			row: strings.Join([]string{
				"\x1b[31ma", "\x1b[32mb", "\x1b[33mc", "\x1b[34md", "\x1b[35me",
				"\x1b[36mf", "\x1b[91mg", "\x1b[92mh", "\x1b[93mi", "\x1b[94mj\x1b[0m",
			}, ""),
			wantErase: true, wantAddressed: true,
		},
		{
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
			if addressed := strings.HasSuffix(got, "H\x1b[K"); addressed != tc.wantAddressed {
				t.Fatalf("addressed=%v, want %v: %q", addressed, tc.wantAddressed, got)
			}
			vt := newVT(w, 1)
			_, _ = vt.Write([]byte("\x1b[1;1H" + strings.Repeat("\u00a4", w)))
			_, _ = vt.Write([]byte(got))
			if strings.Contains(vt.text()[0], "\u00a4") {
				t.Fatalf("stale content survived the row update: %q", vt.text()[0])
			}
		})
	}
}

// A row reaching the final column must still show that column afterwards.
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

// A resync recovers from an undetected foreign write that widened a row, so
// it must still clear every row to the margin; the erase follows the text.
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

	clock = clock.Add(3 * time.Second)
	out.reset()
	tr.paint(frame("status /"))
	got := out.lastFrame()

	if !strings.Contains(got, "status /") {
		t.Fatalf("the frame did not repaint the footer: %q", got)
	}
	if n := strings.Count(got, "\x1b[K"); n != h {
		t.Fatalf("a resync cleared %d row tails, want %d (one per row): %q", n, h, got)
	}
	// The erase must be the last thing each row does.
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
			continue
		}
		if text := strings.TrimSpace(visibleText(rest[:erase])); text == "" {
			t.Fatalf("row %d erased before it wrote anything: %q", r, rest)
		}
	}
}

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
	clock = clock.Add(100 * time.Millisecond)
	out.reset()
	tr.paint(frame("status /"))

	got := out.lastFrame()
	if strings.Contains(got, "row 0") {
		t.Fatalf("a footer tick repainted the body: %q", got)
	}
	if n := strings.Count(got, "\x1b[K"); n != 1 {
		t.Fatalf("a footer tick emitted %d erase(s), want 1 (its own row): %q", n, got)
	}
	if len(got) > 40 {
		t.Fatalf("a footer tick cost %d bytes, want a handful: %q", len(got), got)
	}
}
