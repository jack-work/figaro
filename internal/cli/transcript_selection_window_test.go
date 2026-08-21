package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// TestTranscript_OpenSelectionExtendsIntoHistoryWithoutAGap replaces
// TestTranscript_OpenSelectionLoadsGapBeforeExtending, which is deleted.
//
// The old test pinned a hazard that no longer exists: the pager held a frozen
// copy of the closed tail (t.pages) plus a frozen open message (heldOpen), and
// between them was a GAP: the messages that had closed since the detach, held
// by the client and by nobody else the pager could see. ^P from the open
// message therefore had to refuse to move and ask for a forward page first.
//
// With one owner there is no gap to load: the open turn is the last thing in
// the store's tail interval and the message before it is the one immediately
// before it. So the assertion inverts: ^P from the open message MOVES, at
// once, and asks for nothing.
func TestTranscript_OpenSelectionExtendsIntoHistoryWithoutAGap(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "open-201"},
	}}}}}}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k') // detach: the window keeps its floor, and keeps the live turn

	// Park the viewport on the OPEN message and select it.
	tr.buildIndex()
	_, maxOff := tr.layout(len(tr.footLines()))
	tr.offset = maxOff
	tr.selectNode(-1, false)
	focus := tr.selection.focus
	if focus.turn != 201 {
		t.Fatalf("^P did not seed on the open turn: %+v", focus)
	}
	// While a fresh live message arrives underneath, no less: the released head
	// lands in the same interval, so nothing the selection can see moves.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 1, Nodes: []aria.NodeDelta{{
		ID: 1, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "open-201-more"},
	}}}}}}})
	tr.render()

	tr.selectNode(-1, true)
	if tr.selection.focus == focus {
		t.Fatalf("^P did not extend past the open message: %+v", tr.selection.focus)
	}
}

// TestTranscript_ClearSelectionKeepsTheWindow: clearing a selection that
// spanned the whole window must not move the window. It used to TRIM the
// retained page set in the direction the selection had been dragged; there are
// no pages to trim, and the window is the store's own interval.
func TestTranscript_ClearSelectionKeepsTheWindow(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 3 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	before := tr.messages()
	tr.selection = nodeSelection{
		active: true,
		anchor: testSelectionPoint(before[0].Turn, 0, before[0].Nodes[0]),
		focus:  testSelectionPoint(before[len(before)-1].Turn, 0, before[len(before)-1].Nodes[0]),
	}
	tr.clearSelection()
	after := tr.messages()
	if len(after) != len(before) || after[0].Turn != before[0].Turn ||
		after[len(after)-1].Turn != before[len(before)-1].Turn {
		t.Fatalf("clearing the selection moved the window: [%d..%d] -> [%d..%d]",
			before[0].Turn, before[len(before)-1].Turn, after[0].Turn, after[len(after)-1].Turn)
	}
}

func TestTranscript_LeaveClearsSelection(t *testing.T) {
	history := transcriptHistory(20)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.selectNode(-1, false)
	if !tr.selection.active {
		t.Fatal("precondition: a node should be selected after ^N/^P")
	}
	tr.leave()
	if tr.selection.active {
		t.Error("leaving the pager must drop the selection so none is stranded in incipit where Esc cannot reach it")
	}
}

func TestTranscript_PagedSearchMatchesRenderedMarkdown(t *testing.T) {
	history := transcriptHistory(80)
	history[0].Nodes[0].Markdown = "foo **bar**"
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, &ariaView{settings: &renderSettings{}}, client, "", time.Time{})
	tr.enter()
	tr.find("foo bar")
	for tr.searchingHistory() {
		if !pageOnce(tr, history) {
			break
		}
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "foo") {
		t.Fatalf("rendered Markdown search did not land on match")
	}
}
