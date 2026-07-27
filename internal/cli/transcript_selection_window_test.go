package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// TestTranscript_OpenSelectionExtendsIntoHistoryWithoutAGap is what became of
// TestTranscript_OpenSelectionLoadsGapBeforeExtending.
//
// The old test pinned a hazard that no longer exists: the pager held a frozen
// copy of the closed tail (t.pages) plus a frozen open message (heldOpen), and
// between them was a GAP — the messages that had closed since the detach, held
// by the client and by nobody else the pager could see. ^P from the open
// message therefore had to refuse to move and ask for a forward page first.
//
// With one owner there is no gap to load: the open turn is the last thing in
// the store's tail interval and the message before it is the one immediately
// before it. So the assertion inverts — ^P from the open message MOVES, at
// once, and asks for nothing.
func TestTranscript_OpenSelectionExtendsIntoHistoryWithoutAGap(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
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
	if tr.checkNewer {
		t.Fatal("extending asked for a forward page; there is no gap to fill")
	}
}

func TestTranscript_ClearSelectionKeepsFocusedEdge(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	for i := 0; i < 4; i++ {
		messages := committedMessages(aria.Page{Parts: history[i*30 : (i+1)*30]})
		tr.pages = append(tr.pages, transcriptPage{
			desc:     describePage(messages),
			messages: messages,
		})
	}
	tr.selection = nodeSelection{
		active: true,
		anchor: testSelectionPoint(1, 0, history[0].Nodes[0]),
		focus:  testSelectionPoint(120, 0, history[119].Nodes[0]),
	}
	tr.clearSelection()
	messages := tr.messages()
	if len(tr.pages) != transcriptPageLimit || messages[len(messages)-1].Turn != 120 {
		t.Fatalf("clear retained %d pages ending at LT %d", len(tr.pages), messages[len(messages)-1].Turn)
	}
}

func TestTranscript_LeaveClearsSelection(t *testing.T) {
	history := transcriptHistory(20)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
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
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, &ariaView{settings: &renderSettings{}}, client, "", time.Time{})
	tr.enter()
	tr.find("foo bar")
	for tr.searchingHistory() {
		req, ok := tr.pageCursor()
		if !ok {
			break
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "foo") {
		t.Fatalf("rendered Markdown search did not land on match")
	}
}
