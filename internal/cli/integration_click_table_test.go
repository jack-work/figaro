package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// The gesture seam, end to end: nodeExpandable says whether a node has a
// collapsed form, renderNode draws it at that cap, and the pager's pointer has
// to agree with both. feat/table-wrap and feat/mouse-nodes each owned one half
// and neither could assert the pair, so the walk lives here.
//
// It runs on a TOOL, which is the only node with a collapsed form now; prose's
// inertness is asserted beside it.

func tableProse(rows int) string {
	var b strings.Builder
	b.WriteString("Here is a table.\n\n| id | description | notes |\n|---|---|---|\n")
	for i := range rows {
		fmt.Fprintf(&b, "| %d | a description long enough that the cell must wrap at any sane width %d | note %d |\n", i, i, i)
	}
	return b.String()
}

func toolWithOutput(lines int) livedoc.Node {
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "output line %d\n", i)
	}
	return livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		ToolCallID: "tc1", Summary: "seq 1 40", Output: b.String(),
	}
}

func TestIntegration_ClickOpensAClampedTool(t *testing.T) {
	const width, height = 100, 40
	node := toolWithOutput(40)

	// HALF ONE: the node has a collapsed form at all.
	if !nodeExpandable(node) {
		t.Fatal("a 40-line tool output is not reported expandable: the renderer half of the seam is gone")
	}

	ft := ldrender.NewFakeTerminal(width, height)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Nodes: []livedoc.Node{node},
	}}}})
	tr := newTranscript(ft, width, height, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()

	const hint = "last 10 of 40 lines"
	body := func() string { return stripANSI(strings.Join(tr.lines(), "\n")) }
	if !strings.Contains(body(), hint) {
		t.Fatalf("no clamp hint in the collapsed render:\n%s", body())
	}

	// HALF TWO: the pointer resolves to that node. If this fails, frameRefs
	// (mouse-nodes) regressed.
	ref := nodeRef{turn: 1, index: 0}
	row := clickRowOf(t, tr, ref)

	if !tr.clickAt(row, false) { // select
		t.Fatal("click did not select the tool node")
	}
	if strings.Contains(body(), "output line 0") {
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
	if !strings.Contains(after, "output line 0") {
		t.Fatalf("expansion did not reveal the head of the output:\n%s", after)
	}
	tr.render()

	if !tr.clickAt(clickRowOf(t, tr, ref), false) { // collapse
		t.Fatal("third click did not re-collapse")
	}
	if !strings.Contains(body(), hint) {
		t.Fatalf("the clamp did not come back:\n%s", body())
	}
}

// TestIntegration_ClickOnATableIsInert: a table has nothing to reveal, so
// pointing at it may select it and must never change what it draws.
func TestIntegration_ClickOnATableIsInert(t *testing.T) {
	const width, height = 100, 40
	node := livedoc.Node{Type: livedoc.NodeProse, Markdown: tableProse(14)}

	if nodeExpandable(node) {
		t.Fatal("prose reported expandable: the table clamp is back")
	}

	ft := ldrender.NewFakeTerminal(width, height)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Nodes: []livedoc.Node{node},
	}}}})
	tr := newTranscript(ft, width, height, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()

	body := func() string { return stripANSI(strings.Join(tr.lines(), "\n")) }
	if got := body(); strings.Contains(got, "more table lines") {
		t.Fatalf("the table was clamped:\n%s", got)
	}
	// The last row is on screen from the start: that is the whole point.
	if got := body(); !strings.Contains(got, "wrap at any sane width 13") {
		t.Fatalf("the last table row is not drawn without expanding:\n%s", got)
	}

	// Compare across the TOGGLE, not the selection: the first click draws a
	// selection bar, which is a correct change.
	ref := nodeRef{turn: 1, index: 0}
	tr.clickAt(clickRowOf(t, tr, ref), false) // select
	tr.render()
	selected := body()
	tr.clickAt(clickRowOf(t, tr, ref), false) // would have expanded
	tr.render()
	if got := body(); got != selected {
		t.Errorf("a click changed an unexpandable node's render:\nbefore:\n%s\nafter:\n%s", selected, got)
	}
}

// TestIntegration_TableTextSurvivesTheRoundTrip is the user's actual complaint,
// asserted where the loss used to happen: no cell content missing, no row wider
// than the pane.
func TestIntegration_TableTextSurvivesTheRoundTrip(t *testing.T) {
	for _, width := range []int{60, 80, 100, 140} {
		rows := renderNode(livedoc.Node{Type: livedoc.NodeProse, Markdown: tableProse(6)},
			width, nodeBashCapDefault, 0, false, false)
		joined := stripANSI(strings.Join(rows, "\n"))
		for i := range 6 {
			if !strings.Contains(joined, fmt.Sprintf("note %d", i)) {
				t.Errorf("width %d: last column of row %d was lost:\n%s", width, i, joined)
				break
			}
		}
		// COLUMNS, NOT BYTES. The first version of this assertion used
		// len(stripANSI(r)) and reported every table row as 100+ columns over
		// budget at every width: because a box-drawing glyph (│ ─ ┼) is three
		// BYTES and one COLUMN, and a table is made of them. It read as a
		// width-violation in feat/table-wrap and was a defect in this test:
		// measured properly, the max row width is EXACTLY the budget at 60, 80
		// and 100. Anything that measures a terminal must measure in cells.
		for i, r := range rows {
			if w := runewidth.StringWidth(stripANSI(r)); w > width {
				t.Errorf("width %d: row %d is %d columns wide: one physical line per row is violated", width, i, w)
				break
			}
		}
	}
}
