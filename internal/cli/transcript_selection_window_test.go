package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The open turn is the last thing in the store's tail interval, so ^P from it
// moves at once and asks for nothing.
func TestTranscript_OpenSelectionExtendsIntoHistoryWithoutAGap(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "open-201"},
	}}}}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k')

	tr.buildIndex()
	_, maxOff := tr.layout(len(tr.footLines()))
	tr.offset = maxOff
	tr.selectNode(-1, false)
	focus := tr.selection.focus
	if focus.turn != 201 {
		t.Fatalf("^P did not seed on the open turn: %+v", focus)
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 1, Nodes: []aria.NodeDelta{{
		ID: 1, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "open-201-more"},
	}}}}}}}, aria.Notify)
	tr.render()

	tr.selectNode(-1, true)
	if tr.selection.focus == focus {
		t.Fatalf("^P did not extend past the open message: %+v", tr.selection.focus)
	}
}

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
