package cli

import (
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

	if !tr.toggleSelectedTools() {
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
	if !tr.toggleSelectedTools() {
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
	if !tr.toggleSelectedTools() { // expand
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

	if !tr.toggleSelectedTools() { // collapse
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

	if !tr.toggleSelectedTools() {
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
	if !tr.toggleSelectedTools() {
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
