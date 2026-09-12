package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

func visualFixture(t *testing.T) (*transcript, *ldrender.FakeTerminal) {
	t.Helper()
	ft := ldrender.NewFakeTerminal(60, 16)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "the question", Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "alpha bravo charlie\n\ndelta echo foxtrot"},
			{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: "one\ntwo"},
			{Type: livedoc.NodeProse, Markdown: "golf hotel india"},
		},
	}}}}, aria.Notify)
	tr := newTranscript(ft, 60, 16, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()
	return tr, ft
}

// The first v puts a cursor up and nothing else; motions move it and the
// mode owns the keyboard. Esc leaves.
func TestVisual_FirstPressIsACursorWithNoHighlight(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.key('v')
	if !tr.visual.active() || tr.visual.highlighted() || tr.mode() != modeVisual {
		t.Fatalf("v: active=%v highlighted=%v mode=%v", tr.visual.active(), tr.visual.highlighted(), tr.mode())
	}
	if tr.visualText() != "" {
		t.Fatal("a cursor with no highlight has text")
	}
	line0, _ := tr.visualCursorLine()
	tr.key('k')
	tr.key('k')
	line1, _ := tr.visualCursorLine()
	if line1 >= line0 {
		t.Fatalf("two k did not move the cursor up: %d -> %d", line0, line1)
	}
	tr.key('z') // unbound: inert
	if tr.mode() != modeVisual {
		t.Fatal("an unbound letter left visual mode")
	}
	tr.key(0x1b)
	if tr.visual.active() || tr.mode() != modeTranscript {
		t.Fatalf("Esc did not leave: %+v", tr.visual)
	}
}

// The cursor starts on the first row of the node selection when there is
// one, else on the top row of the bottommost node on screen.
func TestVisual_CursorSeed(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.key('v')
	if got := tr.visual.cursor.ref.index; got != 2 {
		t.Fatalf("with nothing selected the cursor seeded on node %d, want the last node (2)", got)
	}
	if tr.visual.cursor.row != 0 {
		t.Fatalf("seeded on row %d of the node, want its first", tr.visual.cursor.row)
	}
	tr.key(0x1b)
	tr.key(0x0e) // ^N selects a node
	tr.key(0x0e)
	want := tr.selection.focus.nodeRef
	tr.key('v')
	if tr.visual.cursor.ref != want || tr.visual.cursor.row != 0 {
		t.Fatalf("with node %+v selected the cursor seeded at %+v", want, tr.visual.cursor)
	}
	if tr.selection.active {
		t.Fatal("entering visual mode left the node selection up")
	}
}

// v again anchors a character-wise highlight at the cursor; moving extends
// it; v a third time drops it and keeps the cursor. V does the same
// line-wise, and either key switches the kind of a highlight already up.
func TestVisual_SecondPressAnchorsThirdDrops(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.key('v')
	tr.key('v')
	if !tr.visual.highlighted() || tr.visual.kind != visualChar {
		t.Fatalf("second v did not anchor a char highlight: %+v", tr.visual)
	}
	if tr.visual.anchor != tr.visual.cursor {
		t.Fatal("the anchor is not at the cursor")
	}
	tr.key('l')
	tr.key('l')
	if got := len([]rune(tr.visualText())); got != 3 {
		t.Fatalf("anchor plus two l covers %d runes, want 3 (%q)", got, tr.visualText())
	}
	tr.key('V')
	if tr.visual.kind != visualLine {
		t.Fatal("V did not switch the highlight to line-wise")
	}
	tr.key('V')
	if tr.visual.highlighted() || !tr.visual.active() {
		t.Fatalf("V again did not drop the highlight and keep the cursor: %+v", tr.visual)
	}
	tr.key('v')
	tr.key('v')
	if !tr.visual.active() || tr.visual.highlighted() {
		t.Fatalf("v v from a bare cursor anchors then drops, keeping the cursor: %+v", tr.visual)
	}
}

// Motions cross node and message boundaries, stepping over chrome rows.
func TestVisual_CursorCrossesBoundaries(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.key('v')
	seen := map[nodeRef]bool{}
	for i := 0; i < 30; i++ {
		seen[tr.visual.cursor.ref] = true
		line, _ := tr.visualCursorLine()
		if _, ok := tr.visualPointAt(line, 0); !ok {
			t.Fatalf("the cursor stands on a chrome row (%d)", line)
		}
		tr.key('k')
	}
	if len(seen) < 3 {
		t.Fatalf("k walked through %d nodes, want the prose, the tool and the question", len(seen))
	}
	// And the column clamps to the row it lands on.
	tr.key('g')
	tr.key('g')
	for range 200 {
		tr.key('l')
	}
	line, _ := tr.visualCursorLine()
	w := len([]rune(strings.TrimRight(render.StripEscapes(tr.lineText(line)), " ")))
	if tr.visual.cursor.col >= w+1 {
		t.Fatalf("col %d ran past the row's width %d", tr.visual.cursor.col, w)
	}
}

// The cursor cell and the wash are painted at decoration time over cached
// rows, the cursor on top of the wash, and only over the cells they cover.
func TestVisual_PaintCursorAndWash(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	tr, _ := visualFixture(t)
	wash, cur := term.SelectWash(), term.Cursor()
	count := func(needle string) int {
		n := 0
		for _, row := range tr.window(0, tr.index.total, nil) {
			n += strings.Count(row, needle)
		}
		return n
	}
	tr.key('v')
	if count(cur) != 1 || count(wash) != 0 {
		t.Fatalf("cursor only: %d cursor cells, %d wash runs", count(cur), count(wash))
	}
	tr.key('v')
	tr.key('l')
	tr.key('l')
	if count(cur) != 1 || count(wash) < 1 {
		t.Fatalf("char highlight: %d cursor cells, %d wash runs", count(cur), count(wash))
	}
	tr.key('V')
	tr.key('k')
	rowsWashed := 0
	for _, row := range tr.window(0, tr.index.total, nil) {
		if strings.Contains(row, wash) {
			rowsWashed++
		}
	}
	// k stepped over the blank between two nodes; a line-wise highlight
	// washes the blank inside it too, as vim does.
	span := tr.visualSpan()
	if want := span.hiLine - span.loLine + 1; rowsWashed != want || want < 2 {
		t.Fatalf("line highlight over lines %d..%d washed %d rows", span.loLine, span.hiLine, rowsWashed)
	}
	tr.key(0x1b)
	if count(cur) != 0 || count(wash) != 0 {
		t.Fatal("Esc left paint behind")
	}
	for _, c := range tr.rowCache {
		for _, r := range c.rows {
			if strings.Contains(r.text, wash) || strings.Contains(r.text, cur) {
				t.Fatalf("paint was baked into the row cache: %q", r.text)
			}
		}
	}
}

// Node selection and visual mode cannot both be active.
func TestVisual_DropsNodeSelectionAndViceVersa(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.key(0x0e)
	if !tr.selection.active {
		t.Fatal("fixture: ^N did not select a node")
	}
	tr.key('V')
	if tr.selection.active || !tr.visual.active() {
		t.Fatalf("V left the node selection up: sel=%v vis=%v", tr.selection.active, tr.visual.active())
	}
}

// ':' with a highlight up opens the command line already holding the range
// placeholder; with only the cursor it is the plain box.
func TestVisual_ColonPreloadsTheRangeOnlyWhenHighlighted(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.key('V')
	tr.key(':')
	if tr.mode() != modeJump || tr.cmdline.String() != "" {
		t.Fatalf("':' with a bare cursor: mode=%v box=%q", tr.mode(), tr.cmdline.String())
	}
	tr.key(0x1b)
	tr.key('V')
	tr.key(':')
	if got := tr.cmdline.String(); got != visualRangePlaceholder {
		t.Fatalf("box holds %q, want %q", got, visualRangePlaceholder)
	}
	if !tr.visual.highlighted() {
		t.Fatal("opening the box dropped the highlight it was opened for")
	}
}

func TestWashColumns(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	bg, reset := term.SelectWash(), "\x1b[0m"
	cases := []struct {
		row, want string
		from, to  int
	}{
		{"abcdef", "ab" + bg + "cd" + reset + "ef", 2, 4},
		{"ab", bg + "ab  " + reset, 0, 4},                                                 // shorter than the range: padded
		{"a\x1b[1mb\x1b[0mc", bg + "a\x1b[1m" + bg + "b\x1b[0m" + bg + "c" + reset, 0, 3}, // every escape inside re-arms the wash
		{"abc", "abc", 3, 3},                 // empty range: untouched
		{"日本", bg + "日" + reset + "本", 0, 2}, // wide runes count two columns
	}
	for _, c := range cases {
		if got := washColumns(c.row, c.from, c.to); got != c.want {
			t.Errorf("washColumns(%q, %d, %d)\n got %q\nwant %q", c.row, c.from, c.to, got, c.want)
		}
	}
}

func TestSliceColumns(t *testing.T) {
	cases := []struct {
		s, want  string
		from, to int
	}{
		{"abcdef", "cd", 2, 4},
		{"abc", "abc", 0, 10},
		{"abc", "", 5, 9},
		{"日本語", "本", 2, 4},
	}
	for _, c := range cases {
		if got := sliceColumns(c.s, c.from, c.to); got != c.want {
			t.Errorf("sliceColumns(%q, %d, %d) = %q, want %q", c.s, c.from, c.to, got, c.want)
		}
	}
}

// y in visual mode copies what is on screen, at once, and leaves the
// selection up.
func TestVisual_YankCopiesTheVisibleText(t *testing.T) {
	p := newInputProbe(t, true)
	p.in.consume([]byte("V"))
	p.in.consume([]byte("y")) // a bare cursor: refused, nothing copied
	settleProbe(t, p)
	if clip, _ := p.tc.clipboard.Load().(string); clip != "" {
		t.Fatalf("y with no highlight put %q on the clipboard", clip)
	}
	p.in.consume([]byte("V"))
	p.in.consume([]byte("k"))
	p.in.consume([]byte("y"))
	settleProbe(t, p)
	clip, _ := p.tc.clipboard.Load().(string)
	if clip == "" || !strings.Contains(clip, "\n") {
		t.Fatalf("y over a two-row line highlight put %q on the clipboard", clip)
	}
	if !p.lt.tr.visual.highlighted() {
		t.Fatal("y dropped the highlight")
	}
}

func TestVisual_YankCoordinate(t *testing.T) {
	p := newInputProbe(t, true)
	p.in.consume([]byte("V"))
	p.in.consume([]byte("V"))
	p.in.consume([]byte("Y"))
	settleProbe(t, p)
	clip, _ := p.tc.clipboard.Load().(string)
	// The equivalence fixture's nodes carry no Src, so the honest answer is a
	// refusal in the status row and nothing on the clipboard.
	if clip != "" {
		t.Fatalf("Y over a node with no coordinate put %q on the clipboard", clip)
	}
	p.lt.tr.status.mu.Lock()
	note := p.lt.tr.status.notice
	p.lt.tr.status.mu.Unlock()
	if !strings.Contains(note, "coordinate") {
		t.Fatalf("no refusal shown: %q", note)
	}
}

// A cursor inside the wash closes its cell by re-arming the wash, so the rest
// of the row stays highlighted.
func TestVisual_CursorInsideWashRestoresIt(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	wash, cur := term.SelectWash(), term.Cursor()
	got := cursorCell(wash+"abcdef\x1b[0m", 2, wash, 6)
	want := wash + "ab" + cur + "c" + "\x1b[0m" + wash + "def\x1b[0m"
	if got != want {
		t.Fatalf("cursorCell inside a wash\n got %q\nwant %q", got, want)
	}
}
