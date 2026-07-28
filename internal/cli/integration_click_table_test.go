package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// THE MERGE, AS A TEST.
//
// feat/table-wrap gives a wide markdown table a collapsed form. feat/mouse-nodes
// gives the pager a pointer. Neither branch can assert the pair: on table-wrap
// alone nothing opens the clamp, and on mouse-nodes alone no prose node has a
// collapsed form to open. So the claim "the seam held" belongs here, in a test
// that fails if either half regresses — rather than in a merge commit message,
// where a claim cannot fail.
//
// The seam is two names agreed before either branch was built:
//
//	nodeExpandable(n, width) bool                 — has a collapsed form?
//	renderNode(n, w, cap, tick, verbose, expanded) — render it either way
//
// This walks the whole gesture through them: clamp -> click -> open -> click ->
// closed, on a PROSE node, which is the case that did not exist on either
// branch alone.

func tableProse(rows int) string {
	var b strings.Builder
	b.WriteString("Here is a table.\n\n| id | description | notes |\n|---|---|---|\n")
	for i := range rows {
		fmt.Fprintf(&b, "| %d | a description long enough that the cell must wrap at any sane width %d | note %d |\n", i, i, i)
	}
	return b.String()
}

func TestIntegration_ClickOpensAClampedTable(t *testing.T) {
	const width, height = 100, 40
	md := tableProse(14)

	// HALF ONE: the node has a collapsed form at all. If this fails, the clamp
	// (table-wrap) regressed and the gesture has nothing to open.
	node := livedoc.Node{Type: livedoc.NodeProse, Markdown: md}
	if !nodeExpandable(node, width-2) {
		t.Fatal("a 14-row table is not reported expandable: the clamp half of the merge is gone")
	}

	ft := ldrender.NewFakeTerminal(width, height)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Nodes: []livedoc.Node{node},
	}}}})
	tr := newTranscript(ft, width, height, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()

	const hint = "more table lines"
	body := func() string { return stripANSI(strings.Join(tr.lines(), "\n")) }
	if !strings.Contains(body(), hint) {
		t.Fatalf("no clamp hint in the collapsed render:\n%s", body())
	}

	// HALF TWO: the pointer resolves to that node. If this fails, frameRefs
	// (mouse-nodes) regressed.
	ref := nodeRef{turn: 1, index: 0}
	row := clickRowOf(t, tr, ref)

	if !tr.clickAt(row, false) { // select
		t.Fatal("click did not select the table node")
	}
	if strings.Contains(body(), "wrap at any sane width 13") {
		t.Fatal("selecting expanded the node: the two gestures have collapsed into one")
	}
	tr.render()

	if !tr.clickAt(clickRowOf(t, tr, ref), false) { // expand
		t.Fatal("second click did not toggle: the seam between gesture and renderer is broken")
	}
	after := body()
	if strings.Contains(after, hint) {
		t.Fatalf("clamp hint survived expansion:\n%s", after)
	}
	if !strings.Contains(after, "wrap at any sane width 13") {
		t.Fatalf("expansion did not reveal the last table row:\n%s", after)
	}
	tr.render()

	if !tr.clickAt(clickRowOf(t, tr, ref), false) { // collapse
		t.Fatal("third click did not re-collapse")
	}
	if !strings.Contains(body(), hint) {
		t.Fatalf("the clamp did not come back:\n%s", body())
	}
}

// TestIntegration_TableTextSurvivesTheRoundTrip is the user's actual complaint,
// asserted at the boundary the loss used to happen at: no cell content may be
// missing from the EXPANDED render, and no row may exceed the width (invariant
// #1, which a wrapping fix is the most likely thing to break).
func TestIntegration_TableTextSurvivesTheRoundTrip(t *testing.T) {
	for _, width := range []int{60, 80, 100, 140} {
		rows := renderNode(livedoc.Node{Type: livedoc.NodeProse, Markdown: tableProse(6)},
			width, nodeBashCapDefault, 0, false, true)
		joined := stripANSI(strings.Join(rows, "\n"))
		for i := range 6 {
			if !strings.Contains(joined, fmt.Sprintf("note %d", i)) {
				t.Errorf("width %d: last column of row %d was lost:\n%s", width, i, joined)
				break
			}
		}
		// COLUMNS, NOT BYTES. The first version of this assertion used
		// len(stripANSI(r)) and reported every table row as 100+ columns over
		// budget at every width — because a box-drawing glyph (│ ─ ┼) is three
		// BYTES and one COLUMN, and a table is made of them. It read as a
		// width-violation in feat/table-wrap and was a defect in this test:
		// measured properly, the max row width is EXACTLY the budget at 60, 80
		// and 100. Anything that measures a terminal must measure in cells.
		for i, r := range rows {
			if w := runewidth.StringWidth(stripANSI(r)); w > width {
				t.Errorf("width %d: row %d is %d columns wide — one physical line per row is violated", width, i, w)
				break
			}
		}
	}
}
