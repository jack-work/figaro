package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// The wash is background only: a selected row paints the same cells, fills
// the pane to its margin, never grows past it (which would soft-wrap and desync the cursor math),
// and the focused block is washed apart from the range.
func TestDecorateNodeRowContract(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	marks := []selectionMark{
		{selected: true},
		{selected: true, active: true},
		{active: true},
	}
	rows := append([]string{
		"\x1b[2m  │ \x1b[0m   12  internal/cli/transcript.go:7: captured output",
		"✓ bash rg --line-number transcript internal/cli [12ms]",
		"  a glamour-wrapped paragraph row, with its two-column margin",
		"\x1b[38;5;252m  styled from the first byte, margin and all\x1b[0m",
		"a row that is quite a lot longer than the width it will be clipped to",
	}, clipCorpus...)
	for _, row := range rows {
		if !closedEscapes(row) {
			// A row ending inside an escape eats what follows; node
			// rows cannot carry one.
			continue
		}
		for _, w := range []int{-3, 0, 1, 2, 3, 10, 40, 100} {
			plain := plainNodeRow(row, w)
			if got := decorateNodeRow(plain, selectionMark{}, w); got != plain {
				t.Errorf("unselected decorate(%q, %d) = %q, want it untouched", row, w, got)
			}
			for _, mark := range marks {
				got := decorateNodeRow(plain, mark, w)
				if n, pane := displayWidth(got), max(w, 1); n > pane || (w >= 1 && n < pane) {
					t.Errorf("selected decorate(%q, %d) is %d cells; the pane is %d", row, w, n, pane)
				}
				vis := strings.TrimRight(render.StripEscapes(got), " ")
				if want := strings.TrimRight(render.StripEscapes(plain), " "); vis != want {
					t.Errorf("selected decorate(%q, %d) moved the text: %q, want %q", row, w, vis, want)
				}
				if !strings.Contains(got, term.NodeWash(mark.active)) {
					t.Errorf("selected decorate(%q, %d) = %q: no wash", row, w, got)
				}
			}
		}
	}
	if term.NodeWash(true) == term.NodeWash(false) {
		t.Error("the focused block must be washed apart from the range it is in")
	}
}

func closedEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		j, _ := escapeEnd(s, i)
		if j >= len(s) && !isEscapeFinal(s[len(s)-1]) {
			return false
		}
		i = j - 1
	}
	return true
}

func isEscapeFinal(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// The wash is the whole selection UI; without colour there is no cue.
func TestDecorateNodeRowIsWashOnly(t *testing.T) {
	defer term.SetColorMode(term.ColorNever)()
	for _, row := range []string{"  margin row", "✓ bash [1ms]", ""} {
		plain := plainNodeRow(row, 40)
		if got := decorateNodeRow(plain, selectionMark{selected: true}, 40); got != plain {
			t.Errorf("decorate(%q) without colour = %q, want the row untouched", row, got)
		}
	}
}

func TestDecorateNodeRowNoAllocUnmarked(t *testing.T) {
	plain := plainNodeRow("\x1b[2m  │ \x1b[0m a perfectly ordinary tool output row", 100)
	if got := testing.AllocsPerRun(100, func() {
		_ = decorateNodeRow(plain, selectionMark{}, 100)
	}); got != 0 {
		t.Errorf("decorateNodeRow on an unmarked row allocated %v times, want 0", got)
	}
}

func TestTranscriptRowSearchText(t *testing.T) {
	node := transcriptRow{text: plainNodeRow("hello world", 40), ref: nodeRef{turn: 3, index: 0}}
	if got := node.searchText(); got != "hello world" {
		t.Errorf("searchText() = %q, want %q", got, "hello world")
	}
	head := transcriptRow{text: "─── header ───"}
	if got := head.searchText(); got != head.text {
		t.Errorf("searchText() on a ref-less row = %q, want %q", got, head.text)
	}
}

// The frame writer closes every row with its own erase, at a cursor an EL never
// moved: a wash that relied on EL was wiped back to the text's end, and a blank
// row in a selected block showed no wash at all.
func TestWashedRowReachesTheMarginOnScreen(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	const w = 40
	rows := []string{
		"",
		"  ╭ ❝ lt 959.1",
		"\x1b[2m  │ \x1b[0m\x1b[38;5;252mstyled then reset\x1b[39m",
		"  code \x1b[48;5;238m span \x1b[49m then plain",
		"  code \x1b[48;5;238m span \x1b[0;38;5;252m then fg",
		"a row that is quite a lot longer than the forty column pane it lands in",
	}
	for _, row := range rows {
		for _, active := range []bool{false, true} {
			plain := plainNodeRow(row, w)
			line := decorateNodeRow(plain, selectionMark{selected: true, active: active}, w)
			screen := sgrRender(string(appendRowUpdate(nil, 0, w, "", line)), w, 1)
			for x := 0; x < w; x++ {
				c, ok := screen.cells[[2]int{0, x}]
				if !ok || strings.Contains(c.rend, "bg= [") {
					t.Errorf("row %q (active=%v): column %d is unwashed on screen (%+v)", row, active, x, c)
					break
				}
			}
		}
	}
}

func twoTurnPager(t *testing.T) *transcript {
	t.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	turn := func(id int, q string) aria.TurnPart {
		return aria.TurnPart{Turn: aria.Turn{ID: uint64(id), Inquiry: q, Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "first answer of " + q},
			{Type: livedoc.NodeProse, Markdown: "second answer of " + q},
		}}}
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{turn(1, "ONE"), turn(2, "TWO")}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(60, 60), 60, 60,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.offset = 0
	tr.buildIndex()
	return tr
}

// The rule between a question and its reply closes the question: selecting the
// reply's first node washed it too, so the seam lit up for both selections.
func TestASeamRuleBelongsToTheBlockAboveIt(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	for _, tc := range []struct {
		name   string
		ref    nodeRef
		washed []string
		dry    []string
	}{
		{"the reply's first node", nodeRef{turn: 2, index: 0},
			[]string{"< figaro", "first answer of TWO"},
			[]string{"TWO", "second answer of ONE", "> input"}},
		{"the question", nodeRef{turn: 2, index: inquiryNode},
			[]string{"> input", "TWO"},
			[]string{"< figaro", "second answer of ONE"}},
	} {
		tr := twoTurnPager(t)
		tr.selectRef(tc.ref, false)
		rows := tr.window(0, tr.index.total, nil)
		find := func(text string) int {
			for i := len(rows) - 1; i >= 0; i-- {
				if strings.TrimSpace(stripANSI(rows[i])) == text {
					return i
				}
			}
			t.Fatalf("%s: fixture has no row %q:\n%s", tc.name, text, strings.Join(stripANSIAll(rows), "\n"))
			return -1
		}
		for _, text := range tc.washed {
			if !washed(rows[find(text)]) {
				t.Errorf("%s: %q is not washed", tc.name, text)
			}
		}
		for _, text := range tc.dry {
			if washed(rows[find(text)]) {
				t.Errorf("%s: the wash reached %q", tc.name, text)
			}
		}
		voice := find("< figaro")
		seam := rows[voice-1]
		if !isRuleRow(seam) {
			t.Fatalf("%s: fixture: the row above the voice header is not the seam rule: %q", tc.name, stripANSI(seam))
		}
		if want := tc.ref.index == inquiryNode; washed(seam) != want {
			t.Errorf("%s: the seam rule washed = %v, want %v", tc.name, washed(seam), want)
		}
	}
}
