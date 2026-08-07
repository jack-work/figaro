package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// Expanding a tool grows the transcript UPWARD.
//
// The transcript is TEMPORAL: what is higher on screen was generated earlier.
// So a block that changes height must push the EARLIER content off the top and
// leave everything at or after it exactly where it was — that is where the
// reader is looking. t.offset is an absolute line index, so doing nothing
// pinned the viewport top and grew downward instead, shoving later content off
// the bottom.
//
// Observed on the shipped binary before the fix (tmux 100x40, a `seq 1 400`
// tool truncated to 10 of 200 lines with a one-line reply under it): the reply
// sat on screen row 38, and pressing Enter moved the window from 33-70/70 to
// 219-256/259 — the reply was not merely displaced, it was gone from the
// screen, and the reader was dropped into the middle of the tool output.
// ---------------------------------------------------------------------------

// expandFixture is a transcript over one turn whose middle node is a tool with
// output long enough to be truncated, followed by a distinctive prose node —
// the anchor whose screen row must not move.
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
	}})
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
	return tr, nodeRef{turn: 2, index: 2} // the SENTINEL prose node
}

// screenRowOf returns the anchor's offset from the viewport top, or -1 when it
// is not on screen. That is the number the reader perceives.
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

	// Park the viewport so the tool AND the sentinel under it are both on
	// screen, the way a reader who is about to press Enter has them.
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

	// COLLAPSING IS THE INVERSE, verified rather than assumed: the gap closes
	// upward and the same anchor keeps the same row.
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

// TestExpandGrowsUpward_Clamps pins the low clamp, which is the reachable one.
//
// The two directions are opposite and easy to get backwards — the first draft
// of this test asserted a LOW clamp on an EXPAND, which is simply wrong.
// Expanding moves the offset DOWN, and line space and maxOff grow by the same
// number of rows, so a viewport that was in range stays in range: the high
// clamp essentially cannot fire. Collapsing moves the offset UP, and a change
// straddling the viewport top can ask it to go above line 0.
func TestExpandGrowsUpward_Clamps(t *testing.T) {
	tr, _ := expandFixture(t)
	tr.buildIndex()
	_, maxOff := tr.layout(len(tr.footLines()))
	tr.offset = maxOff
	if !tr.toggleSelectedNodes() { // expand
		t.Fatal("fixture: the tool did not expand")
	}
	// Park INSIDE the expanded block, so it straddles the viewport top: the
	// collapse then removes far more rows than there are above the offset.
	tr.buildIndex()
	span, ok := tr.nodeSpanOf(nodeRef{turn: 2, index: 1})
	if !ok {
		t.Fatal("fixture: no span for the expanded tool")
	}
	tr.offset = span.first + 20
	if tr.offset >= span.last {
		t.Fatal("fixture: the expanded tool must be taller than the parking offset")
	}

	if !tr.toggleSelectedNodes() { // collapse
		t.Fatal("the tool did not collapse")
	}
	if tr.offset < 0 {
		t.Fatalf("collapse drove the offset negative: %d", tr.offset)
	}
	if tr.offset != 0 {
		t.Fatalf("collapse wanted to scroll above line 0; want the clamp at 0, got %d", tr.offset)
	}
}

// TestExpandBelowViewportLeavesItAlone: the rule is that content at or below
// the change holds still. When NONE of that content is on screen there is
// nothing to hold, and moving anyway would scroll the reader away from a part
// of the transcript that did not change. Reachable by selecting a tool and
// scrolling up away from it before pressing Enter.
func TestExpandBelowViewportLeavesItAlone(t *testing.T) {
	tr, _ := expandFixture(t)
	tr.buildIndex()
	tr.offset = 0 // the tool is far below the viewport
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

// TestExpandInFollowModeStaysPinned: following pins the viewport to the bottom
// every frame, so the anchor shift must not fight it.
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

// ---------------------------------------------------------------------------
// ensureSelectionVisible must agree with layout() about the body height.
//
// It recomputed the body as t.h - 1 while layout() reserves two rows for the
// footer, one per open-panel line, and one more for the live padding row. So it
// scrolled just short and left the focus off the bottom edge — a selection that
// moved the page and then wasn't on it.
// ---------------------------------------------------------------------------

// focusVisible reports whether the focused node is inside the body as layout()
// defines it. This is the reader's question: is the thing I just selected on
// the screen.
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
		{"status panel open", func(tr *transcript) { tr.showStatus = true }},
		{"help panel open", func(tr *transcript) { tr.showHelp = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := expandFixture(t)
			tc.panel(tr)
			tr.offset = 0

			// Focus the LAST selectable node, forcing a downward scroll-into-view.
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

// ---------------------------------------------------------------------------
// Selecting a STREAMING tool and pressing Enter opens its arguments, and the
// window keeps streaming underneath the selection.
//
// The gesture used to be inert on exactly this node: nodeExpandable answered
// "has output?", and a tool whose arguments are still arriving has none yet —
// so the one block a reader most wants to open (a running write, to watch the
// file arrive) was the one Enter did nothing to.
// ---------------------------------------------------------------------------

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
	}})
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
			// The fixture's tool has no output, so every content row inside
			// the box is an argument row.
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
		t.Fatal("Enter was inert on a streaming tool — nodeExpandable must answer for arguments")
	}
	expanded := argRowsOf(tr)
	if len(expanded) <= len(folded) {
		t.Fatalf("expanding revealed nothing: %d rows before, %d after", len(folded), len(expanded))
	}

	// Collapsing returns to the window, so the gesture is a toggle rather than
	// a one-way door.
	if !tr.toggleSelectedNodes() {
		t.Fatal("second Enter did not collapse")
	}
	if got := len(argRowsOf(tr)); got != len(folded) {
		t.Fatalf("collapse: %d argument rows, want %d", got, len(folded))
	}
}

// The window follows the stream: as patches arrive, the two rows on screen are
// the two most recent, not the two oldest.
func TestPagerStreamingWindowFollowsTheStream(t *testing.T) {
	// More lines than the body's clamp holds, so it has something to roll past.
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
	}})
	tr.rowCache = map[sliceKey]cachedMessage{}

	after := strings.Join(argRowsOf(tr), "\n")
	if !strings.Contains(after, fmt.Sprintf("%d. line", nodeBashCapDefault+4)) {
		t.Errorf("window did not follow the stream:\n%s", after)
	}
	if strings.Contains(after, "│ 1. line of the file") {
		t.Errorf("window should have rolled past the first line:\n%s", after)
	}
}

// Escape clears the SELECTION and leaves the EXPANSION alone. Collapsing is a
// deliberate act — select the node again and press Enter — not a side effect
// of pointing somewhere else.
//
// The bug underneath was worse than the gesture: pruneCaches walks the store's
// window to decide what to keep, and the OPEN turn is not in that window. Its
// caches were therefore pruned as though it had scrolled out of history. The
// row cache merely re-renders, but `expanded` is user state, and it was being
// dropped on Escape — and on EVERY FRAME while following the live tail, which
// is why expanding a streaming tool looked like it did not work.
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

	// Following the tail prunes once per frame; the expansion must survive it.
	tr.follow = true
	tr.resetToTail()
	if !tr.expanded[ref] {
		t.Error("a frame while following dropped the expansion")
	}

	// And selecting it again collapses it — the only way back.
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

// expandedHashProbe is the node hash the selection endpoints carry, looked up
// by ref — the tests build selections by hand and must agree with the guard.
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

// A SEPARATOR MARKS A TURN BOUNDARY, NOT AN ENTRY BOUNDARY.
//
// One turn can be several entries: a long agentic turn is delivered in slices,
// and paging back into it delivers more. A rule between those slices claims
// another exchange began where none did — and since the question is drawn only
// on the slice that STARTS a turn, every later slice showed a rule, a
// `< figaro` header and no question. That is what "the transcript omits
// inquiries" turned out to be.
//
// Measured from the owner's own recorded tape of the failing case: ONE turn,
// two slices at offsets 74 and 64 of a 130-node turn, both carrying the
// inquiry on the wire, neither entitled to draw it.
func TestSeparatorOnlyAtATurnBoundary(t *testing.T) {
	long := make([]livedoc.Node, 0, 12)
	for i := range 12 {
		long = append(long, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("node %d", i)})
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	// Turn 9 arrives as TWO slices, as a long turn does; turn 10 is its own.
	// The tail slice arrives first, as a backward page delivers it, then the
	// page holding the head — which is exactly the owner's recorded case.
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 9, Inquiry: "the long question", Sealed: true, Nodes: long[6:]},
			From: 6, ClippedHead: true},
	}})
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 9, Inquiry: "the long question", Sealed: true, Nodes: long[:6]}, From: 0},
	}})
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 10, Inquiry: "the next question", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answer"}}}, From: 0},
	}})

	tr := newTranscript(ldrender.NewFakeTerminal(60, 24), 60, 24,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	var seps, entries int
	byTurn := map[int]int{}
	for _, e := range tr.index.entries {
		entries++
		byTurn[e.turn]++
		if e.sep {
			seps++
		}
	}
	if byTurn[9] < 2 {
		t.Fatalf("fixture: turn 9 should be several entries, got %d", byTurn[9])
	}
	// The first entry never gets one, so N turns give N-1 separators however
	// many entries they are spread across.
	if want := len(byTurn) - 1; seps != want {
		t.Errorf("%d separators across %d entries of %d turns, want %d — a rule between "+
			"slices of one turn reads as a turn that lost its question",
			seps, entries, len(byTurn), want)
	}
}
