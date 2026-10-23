package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The transcript is temporal, so a block that changes height pushes earlier
// content off the top and leaves everything at or after it where it was.

func expandFixture(t *testing.T) (*transcript, nodeRef) {
	t.Helper()
	nodes := []livedoc.Node{
		{Type: livedoc.NodeProse, Markdown: "before the tool"},
		{
			Type: livedoc.NodeTool, ID: "t1", Name: "bash",
			Args:    map[string]any{"command": "seq 1 400"},
			Status:  livedoc.StatusOK,
			Summary: "seq 1 400",
			Output:  strings.TrimRight(strings.Repeat("line of output\n", 300), "\n"),
		},
		{Type: livedoc.NodeProse, Markdown: "SENTINEL"},
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "filler", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("padding line\n\n", 20)}}}},
		{Turn: aria.Turn{ID: 2, Inquiry: "please look", Sealed: true, Nodes: nodes}},
	}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(60, 24), 60, 24, &ariaView{settings: &renderSettings{}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	toolRef := nodeRef{turn: 2, index: 1}
	tr.selection = nodeSelection{
		active: true,
		anchor: selectionPoint{nodeRef: toolRef, hash: nodeHash(nodes[1])},
		focus:  selectionPoint{nodeRef: toolRef, hash: nodeHash(nodes[1])},
	}
	return tr, nodeRef{turn: 2, index: 2}
}

func screenRowOf(tr *transcript, ref nodeRef) int {
	tr.buildIndex()
	span, ok := tr.nodeSpanOf(ref)
	if !ok {
		return -1
	}
	body, _ := tr.layout(len(tr.footLines()))
	row := span.first - tr.offset
	if row < 0 || row >= body {
		return -1
	}
	return row
}

func TestExpandGrowsUpward_AnchorKeepsItsScreenRow(t *testing.T) {
	tr, sentinel := expandFixture(t)

	tr.buildIndex()
	_, maxOff := tr.layout(len(tr.footLines()))
	tr.offset = maxOff

	before := screenRowOf(tr, sentinel)
	if before < 0 {
		t.Fatal("fixture: the sentinel must be on screen before expanding")
	}
	beforeOffset := tr.offset

	if !tr.toggleSelectedNodes() {
		t.Fatal("fixture: the tool did not expand")
	}
	if tr.offset == beforeOffset {
		t.Fatal("expanding did not move the viewport at all: the content below was pushed down")
	}
	if got := screenRowOf(tr, sentinel); got != before {
		t.Fatalf("expanding moved the sentinel from screen row %d to %d; "+
			"content at or below the expansion must hold still", before, got)
	}

	if !tr.toggleSelectedNodes() {
		t.Fatal("the tool did not collapse")
	}
	if got := screenRowOf(tr, sentinel); got != before {
		t.Fatalf("collapsing moved the sentinel from screen row %d to %d", before, got)
	}
	if tr.offset != beforeOffset {
		t.Fatalf("expand+collapse round trip left the viewport at %d, want %d",
			tr.offset, beforeOffset)
	}
}

// Expanding moves the offset down and maxOff grows with it, so only the
// collapse can ask to go above line 0.
func TestExpandGrowsUpward_Clamps(t *testing.T) {
	tr, _ := expandFixture(t)
	tr.buildIndex()
	_, maxOff := tr.layout(len(tr.footLines()))
	tr.offset = maxOff
	if !tr.toggleSelectedNodes() {
		t.Fatal("fixture: the tool did not expand")
	}
	// Parked inside the expanded block, the collapse removes more rows than
	// there are above the offset.
	tr.buildIndex()
	span, ok := tr.nodeSpanOf(nodeRef{turn: 2, index: 1})
	if !ok {
		t.Fatal("fixture: no span for the expanded tool")
	}
	tr.offset = span.first + 20
	if tr.offset >= span.last {
		t.Fatal("fixture: the expanded tool must be taller than the parking offset")
	}

	if !tr.toggleSelectedNodes() {
		t.Fatal("the tool did not collapse")
	}
	if tr.offset < 0 {
		t.Fatalf("collapse drove the offset negative: %d", tr.offset)
	}
	if tr.offset != 0 {
		t.Fatalf("collapse wanted to scroll above line 0; want the clamp at 0, got %d", tr.offset)
	}
}

// With none of the content at or below the change on screen, nothing holds.
func TestExpandBelowViewportLeavesItAlone(t *testing.T) {
	tr, _ := expandFixture(t)
	tr.buildIndex()
	tr.offset = 0
	_, bottom := tr.viewportLines()
	span, ok := tr.nodeSpanOf(nodeRef{turn: 2, index: 1})
	if !ok || span.first < bottom {
		t.Fatalf("fixture: the tool must start below the viewport (first=%d bottom=%d)", span.first, bottom)
	}

	if !tr.toggleSelectedNodes() {
		t.Fatal("fixture: the tool did not expand")
	}
	if tr.offset != 0 {
		t.Fatalf("a change entirely below the viewport moved it to %d, want 0", tr.offset)
	}
}

func TestExpandInFollowModeStaysPinned(t *testing.T) {
	tr, _ := expandFixture(t)
	tr.follow = true
	before := tr.offset
	if !tr.toggleSelectedNodes() {
		t.Fatal("fixture: the tool did not expand")
	}
	tr.render()
	tr.buildIndex()
	_, maxOff := tr.layout(len(tr.footLines()))
	if tr.offset != maxOff {
		t.Fatalf("following: viewport at %d, want the tail %d (was %d)", tr.offset, maxOff, before)
	}
}

// ensureSelectionVisible must use the body height layout() computes.
func focusVisible(tr *transcript) (bool, nodeSpan, int, int) {
	tr.buildIndex()
	span, ok := tr.nodeSpanOf(tr.selection.focus.nodeRef)
	if !ok {
		return false, span, 0, 0
	}
	body, _ := tr.layout(len(tr.footLines()))
	return span.last < tr.offset+body && span.first >= tr.offset, span, tr.offset, body
}

func TestEnsureSelectionVisibleUsesLayoutBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		panel func(*transcript)
	}{
		{"no panel", func(*transcript) {}},
		{"status panel open", func(tr *transcript) { tr.openStatusPit() }},
		{"help panel open", func(tr *transcript) { tr.openHelpPit() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := expandFixture(t)
			tc.panel(tr)
			tr.offset = 0

			refs := tr.nodeRefs()
			last := refs[len(refs)-1]
			tr.selection = nodeSelection{active: true, anchor: last, focus: last}
			tr.ensureSelectionVisible()

			ok, span, off, body := focusVisible(tr)
			if !ok {
				t.Fatalf("focus not on screen after scrolling to it: span=%v offset=%d body=%d "+
					"(bottom edge %d)", span, off, body, off+body-1)
			}
		})
	}
}

// Enter on a streaming tool opens its arguments, which keep streaming.
func streamingFixture(t *testing.T, body string) (*transcript, nodeRef, livedoc.Node) {
	t.Helper()
	tool := livedoc.Node{
		Type: livedoc.NodeTool, ID: "t1", Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x.md","content":"` + body,
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "write it", Sealed: false,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "on it"}, tool}}},
	}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(80, 24), 80, 24, &ariaView{settings: &renderSettings{}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	ref := nodeRef{turn: 1, index: 1}
	tr.selection = nodeSelection{
		active: true,
		anchor: selectionPoint{nodeRef: ref, hash: nodeHash(tool)},
		focus:  selectionPoint{nodeRef: ref, hash: nodeHash(tool)},
	}
	return tr, ref, tool
}

func argRowsOf(tr *transcript) []string {
	tr.buildIndex()
	var out []string
	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			if plain := stripANSI(r.text); boxContentText(plain) != "" {
				out = append(out, plain)
			}
		}
	}
	return out
}

func TestPagerStreamingToolExpandsItsArguments(t *testing.T) {
	tr, _, _ := streamingFixture(t, strings.Repeat("a line of the file being written\n", 30))

	folded := argRowsOf(tr)
	if len(folded) == 0 {
		t.Fatal("fixture: the streaming tool drew no content")
	}

	if !tr.toggleSelectedNodes() {
		t.Fatal("Enter was inert on a streaming tool: nodeExpandable must answer for arguments")
	}
	expanded := argRowsOf(tr)
	if len(expanded) <= len(folded) {
		t.Fatalf("expanding revealed nothing: %d rows before, %d after", len(folded), len(expanded))
	}

	if !tr.toggleSelectedNodes() {
		t.Fatal("second Enter did not collapse")
	}
	if got := len(argRowsOf(tr)); got != len(folded) {
		t.Fatalf("collapse: %d argument rows, want %d", got, len(folded))
	}
}

func TestPagerStreamingWindowFollowsTheStream(t *testing.T) {
	var seed strings.Builder
	for i := 1; i <= nodeBashCapDefault+2; i++ {
		fmt.Fprintf(&seed, "%d. line of the file\n", i)
	}
	tr, _, tool := streamingFixture(t, seed.String())
	before := argRowsOf(tr)
	if !strings.Contains(strings.Join(before, "\n"), fmt.Sprintf("%d. line", nodeBashCapDefault+2)) {
		t.Fatalf("window should hold the newest lines:\n%s", strings.Join(before, "\n"))
	}

	tool.Input += fmt.Sprintf("%d. line of the file\n%d. line of the file\n", nodeBashCapDefault+3, nodeBashCapDefault+4)
	tr.client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "write it", Sealed: false,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "on it"}, tool}}},
	}}, aria.Notify)
	tr.rowCache = map[sliceKey]cachedMessage{}

	after := strings.Join(argRowsOf(tr), "\n")
	if !strings.Contains(after, fmt.Sprintf("%d. line", nodeBashCapDefault+4)) {
		t.Errorf("window did not follow the stream:\n%s", after)
	}
	if strings.Contains(after, "│ 1. line of the file") {
		t.Errorf("window should have rolled past the first line:\n%s", after)
	}
}

// Escape clears the selection and leaves the expansion alone. The open turn
// is not in the store window, so pruneCaches must not drop its user state.
func TestEscapeKeepsExpansionOnTheOpenTurn(t *testing.T) {
	tr, ref, _ := streamingFixture(t, strings.Repeat("a line of the file being written\n", 30))
	if !tr.toggleSelectedNodes() {
		t.Fatal("fixture: the tool did not expand")
	}
	expanded := len(argRowsOf(tr))

	tr.clearSelection()
	if !tr.expanded[ref] {
		t.Error("Escape dropped the expansion")
	}
	if got := len(argRowsOf(tr)); got != expanded {
		t.Errorf("after Escape: %d argument rows, want the expanded %d", got, expanded)
	}
	if tr.selection.active {
		t.Error("Escape should still clear the selection")
	}

	// Following the tail prunes once per frame.
	tr.follow = true
	tr.resetToTail()
	if !tr.expanded[ref] {
		t.Error("a frame while following dropped the expansion")
	}

	tr.selection = nodeSelection{active: true,
		anchor: selectionPoint{nodeRef: ref, hash: tr.expandedHashProbe(ref)},
		focus:  selectionPoint{nodeRef: ref, hash: tr.expandedHashProbe(ref)}}
	if !tr.toggleSelectedNodes() {
		t.Fatal("re-selecting did not toggle")
	}
	if tr.expanded[ref] {
		t.Error("Enter on the selected node should collapse it again")
	}
}

func (t *transcript) expandedHashProbe(ref nodeRef) uint64 {
	var h uint64
	if open := t.openMessage(); open != nil && open.Turn == ref.turn {
		for i, n := range open.Nodes {
			if nodeRefAt(*open, i) == ref {
				h = nodeHash(n)
			}
		}
	}
	return h
}

// A separator marks a turn boundary, not an entry boundary: a long turn
// arrives in slices and only the slice that starts a turn draws the question.
func TestSeparatorOnlyAtATurnBoundary(t *testing.T) {
	long := make([]livedoc.Node, 0, 12)
	for i := range 12 {
		long = append(long, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("node %d", i)})
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	// The tail slice arrives first, as a backward page delivers it.
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 9, Inquiry: "the long question", Sealed: true, Nodes: long[6:]},
			From: 6, ClippedHead: true},
	}}, aria.Notify)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 9, Inquiry: "the long question", Sealed: true, Nodes: long[:6]}, From: 0},
	}}, aria.Notify)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 10, Inquiry: "the next question", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answer"}}}, From: 0},
	}}, aria.Notify)

	tr := newTranscript(ldrender.NewFakeTerminal(60, 24), 60, 24,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	var seps, entries int
	byTurn := map[int]int{}
	for k, e := range tr.index.entries {
		entries++
		byTurn[e.turn]++
		if e.seam == seamTurn {
			seps++
		}
		if k > 0 && e.turn == tr.index.entries[k-1].turn && e.seam != seamSlice {
			t.Errorf("slice %d continues turn %d with seam %d: its first node sits directly "+
				"under the previous slice's last row", k, e.turn, e.seam)
		}
	}
	if byTurn[9] < 2 {
		t.Fatalf("fixture: turn 9 should be several entries, got %d", byTurn[9])
	}
	if want := len(byTurn) - 1; seps != want {
		t.Errorf("%d separators across %d entries of %d turns, want %d, a rule between "+
			"slices of one turn reads as a turn that lost its question",
			seps, entries, len(byTurn), want)
	}
}
