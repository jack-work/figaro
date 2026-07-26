package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestTranscript_ForwardSelectionRequestsEvictedPage(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 4 {
		tr.offset = 0
		tr.checkOlder = true
		req, _ := tr.pageCursor()
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}

	messages := tr.messages()
	tr.selection = nodeSelection{
		active: true,
		anchor: testSelectionPoint(messages[len(messages)-1].Turn, 0, messages[len(messages)-1].Nodes[0]),
		focus:  testSelectionPoint(messages[len(messages)-1].Turn, 0, messages[len(messages)-1].Nodes[0]),
	}
	tr.offset = len(tr.lineTurn)
	tr.selectNode(1, true)
	req, ok := tr.pageCursor()
	if !ok || req.direction != pageNewer {
		t.Fatalf("forward selection page request = %+v, %v", req, ok)
	}
}

func TestTranscript_ScrollingPinsOpenMessage(t *testing.T) {
	client := aria.NewClient()
	history := transcriptHistory(2)
	client.Apply(aria.Page{Parts: history})
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(3), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "still streaming"},
	}}}}}}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k')
	if tr.heldOpen == nil || tr.openMessage() == nil {
		t.Fatal("scrolling dropped the open message")
	}
}

func TestTranscript_OpenRangeRehydratesEvictedPages(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(121), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "open-121"},
	}}}}}}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.selectNode(-1, false)
	for range 3 {
		first := tr.messages()[0]
		tr.selection.focus = testSelectionPoint(first.Turn, 0, first.Nodes[0])
		tr.offset = 0
		tr.checkOlder = true
		req, ok := tr.pageCursor()
		if !ok {
			t.Fatal("expected older page")
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	first := tr.messages()[0]
	tr.selection.focus = testSelectionPoint(first.Turn, 0, first.Nodes[0])
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("open range retained %d payload pages", len(tr.pages))
	}
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("open selection endpoints were lost")
	}
	text, err := selectionText(plan, transcriptPageSize, func(before, limit int) (aria.Page, error) {
		return readBefore(history, before, limit), nil
	})
	if err != nil || !strings.Contains(text, "message-001") || !strings.Contains(text, "open-121") {
		t.Fatalf("open range copy = %q, %v", text, err)
	}
}
