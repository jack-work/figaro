package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// nodeExpandable, renderNode's clamp and the pager's pointer must agree.

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

	if !nodeExpandable(node) {
		t.Fatal("a 40-line tool output is not reported expandable: the renderer half of the seam is gone")
	}

	ft := ldrender.NewFakeTerminal(width, height)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Nodes: []livedoc.Node{node},
	}}}}, aria.Notify)
	tr := newTranscript(ft, width, height, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()

	const hint = "last 10 of 40 lines"
	body := func() string { return stripANSI(strings.Join(tr.lines(), "\n")) }
	if !strings.Contains(body(), hint) {
		t.Fatalf("no clamp hint in the collapsed render:\n%s", body())
	}

	ref := nodeRef{turn: 1, index: 0}
	row := clickRowOf(t, tr, ref)

	if !tr.clickAt(row, false) {
		t.Fatal("click did not select the tool node")
	}
	if strings.Contains(body(), "output line 0") {
		t.Fatal("selecting expanded the node: the two gestures have collapsed into one")
	}
	tr.render()

	if !tr.clickAt(clickRowOf(t, tr, ref), false) {
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

	if !tr.clickAt(clickRowOf(t, tr, ref), false) {
		t.Fatal("third click did not re-collapse")
	}
	if !strings.Contains(body(), hint) {
		t.Fatalf("the clamp did not come back:\n%s", body())
	}
}

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
	}}}}, aria.Notify)
	tr := newTranscript(ft, width, height, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()

	body := func() string { return stripANSI(strings.Join(tr.lines(), "\n")) }
	if got := body(); strings.Contains(got, "more table lines") {
		t.Fatalf("the table was clamped:\n%s", got)
	}
	if got := body(); !strings.Contains(got, "wrap at any sane width 13") {
		t.Fatalf("the last table row is not drawn without expanding:\n%s", got)
	}

	// Compare across the toggle, not the selection: the first click draws a bar.
	ref := nodeRef{turn: 1, index: 0}
	tr.clickAt(clickRowOf(t, tr, ref), false)
	tr.render()
	selected := body()
	tr.clickAt(clickRowOf(t, tr, ref), false)
	tr.render()
	table := func(s string) string {
		var rows []string
		for _, l := range strings.Split(s, "\n") {
			if strings.Contains(l, "│") || strings.Contains(l, "┼") {
				rows = append(rows, strings.TrimLeft(l, "•╭│ "))
			}
		}
		return strings.Join(rows, "\n")
	}
	if got := body(); table(got) != table(selected) || strings.Contains(got, "more table lines") {
		t.Errorf("a click changed the table itself:\nbefore:\n%s\nafter:\n%s", selected, got)
	}
}

func TestIntegration_TableTextSurvivesTheRoundTrip(t *testing.T) {
	for _, width := range []int{60, 80, 100, 140} {
		rows := renderNode(livedoc.Node{Type: livedoc.NodeProse, Markdown: tableProse(6)},
			width, nodeBashCapDefault, 0, false)
		joined := stripANSI(strings.Join(rows, "\n"))
		for i := range 6 {
			if !strings.Contains(joined, fmt.Sprintf("note %d", i)) {
				t.Errorf("width %d: last column of row %d was lost:\n%s", width, i, joined)
				break
			}
		}
		// Measure in cells: a box-drawing glyph is three bytes and one column.
		for i, r := range rows {
			if w := runewidth.StringWidth(stripANSI(r)); w > width {
				t.Errorf("width %d: row %d is %d columns wide: one physical line per row is violated", width, i, w)
				break
			}
		}
	}
}
