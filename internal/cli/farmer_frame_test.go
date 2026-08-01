package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// farmerTranscript builds a transcript over thinking-heavy content — the frame
// is the thing the owner actually looks at, and node rows are only half of it
// (the transcript renders nodes at t.w-2 and re-clips at t.w-1 through
// plainNodeRow, so a node-level invariant is not a frame-level one).
func farmerTranscript(t *testing.T, w, h int) *transcript {
	t.Helper()
	long := "I'll need to read through the plan document and review the diffs to give a " +
		"comprehensive response about the repository, the worktree setup, console issues."
	nodes := []livedoc.Node{
		{Type: livedoc.NodeThinking, Markdown: long},
		{Type: livedoc.NodeProse, Markdown: "plain answer, " + long},
		{Type: livedoc.NodeSteering, Markdown: "- a steering list item long enough to wrap\n- and another\n\n" + long},
		{Type: livedoc.NodeThinking, Markdown: "```go\nfunc main() { println(\"a long line of code that will not wrap nicely\") }\n```\n\n" + long},
		{Type: livedoc.NodeThinking, Markdown: "日本語のテキストをここに書きます。" + long},
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "please look", Sealed: true, Nodes: nodes}},
	}})
	ft := ldrender.NewFakeTerminal(w, h)
	tr := newTranscript(ft, w, h, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	return tr
}

// TestFarmerFrameRowsFitEveryWidth: no painted row may exceed the viewport, at
// any width, in a frame — plain, selected, and search-highlighted.
func TestFarmerFrameRowsFitEveryWidth(t *testing.T) {
	seen := map[string]bool{}
	for w := 20; w <= 200; w++ {
		tr := farmerTranscript(t, w, 20)
		check := func(state string, rows []string) {
			for i, r := range rows {
				if d := displayWidth(r); d > w {
					if !seen[state] {
						seen[state] = true
						t.Errorf("%s w=%d row %d is %d cells: %q", state, w, i, d, stripANSI(r))
					}
				}
			}
		}
		check("plain", tr.lines())
		tr.offset = 0
		tr.selectNode(1, false)
		tr.selectNode(1, false)
		check("selected", tr.lines())
		tr.clearSelection()
		tr.matchQuery = "plan"
		check("highlighted", tr.lines())
	}
}

// TestFarmerResizeIsNotRepair: a transcript resized DOWN then back must paint
// the same frame as one born at that width. A fuzzer whose own driving heals
// the defect reports clean against broken code, so this compares the resized
// frame against a freshly built one rather than merely re-checking invariants.
func TestFarmerResizeStable(t *testing.T) {
	for _, path := range [][]int{
		{80, 40, 80}, {80, 200, 80}, {61, 62, 61}, {100, 20, 100}, {73, 74, 73},
	} {
		start := path[0]
		tr := farmerTranscript(t, start, 20)
		want := append([]string(nil), tr.lines()...)
		for _, w := range path[1:] {
			tr.resize(w, 20)
		}
		got := tr.lines()
		if len(got) != len(want) {
			t.Errorf("path %v: %d rows after the round trip, %d before", path, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("path %v: row %d differs after the round trip:\n  before %q\n   after %q",
					path, i, stripANSI(want[i]), stripANSI(got[i]))
				break
			}
		}
	}
}

// TestFarmerFrameRuleColumn: in the FRAME, every rule of one thinking block
// must still sit in one column. The node-level test cannot see the transcript's
// own gutter column or its second clip.
func TestFarmerFrameRuleColumn(t *testing.T) {
	seen := false
	for w := 20; w <= 200; w++ {
		tr := farmerTranscript(t, w, 60)
		col, blockOpen := -1, false
		for i, r := range tr.lines() {
			plain := strings.TrimRight(stripANSI(r), " ")
			if strings.TrimSpace(plain) == "" {
				col, blockOpen = -1, false // a blank row ends the block
				continue
			}
			at := strings.Index(plain, "│")
			if at < 0 {
				// Inside an OPEN block a row with no rule IS the original
				// defect, and skipping it was this test's own bug: with the
				// repair neutered entirely the test still passed, because a
				// gutterless row merely closed the block.
				if blockOpen && !seen {
					seen = true
					t.Errorf("w=%d row %d: no rule inside an open block: %q", w, i, plain)
				}
				col, blockOpen = -1, false
				continue
			}
			// count columns, not bytes, up to the rule
			c := displayWidth(plain[:at])
			if !blockOpen {
				col, blockOpen = c, true
				continue
			}
			if c != col && !seen {
				seen = true
				t.Errorf("w=%d row %d: rule at column %d, block uses %d: %q", w, i, c, col, plain)
			}
		}
	}
}
