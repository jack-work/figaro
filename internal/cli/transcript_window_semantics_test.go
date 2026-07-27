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
	tr.offset = len(tr.lineKey)
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
	// PINNED, NOT FROZEN. Scrolling away keeps the open message in the window —
	// it is the last thing in the store's tail interval — and it keeps arriving
	// from the client rather than from a snapshot taken at the detach.
	open := tr.openMessage()
	if open == nil {
		t.Fatal("scrolling dropped the open message")
	}
	if open.Nodes[0].Markdown != "still streaming" {
		t.Fatalf("open message = %+v", open.Nodes)
	}
}

// TestTranscript_LongRangeRehydratesEvictedPages: a selection whose two ends
// are pages apart still copies, by re-reading the range from the server.
//
// It used to anchor the far end on the OPEN message, which the pager kept
// beside its frozen window as heldOpen. Paging history in now takes the window
// off the store's tail (openMessage goes quiet rather than drawing a snapshot
// that history has run past), so the live turn is not an endpoint you can hold
// while browsing a hundred turns above it. That is a real narrowing, and it
// lasts exactly until t.pages is gone: with the whole window an interval into
// the store, both ends are in the same structure again.
func TestTranscript_LongRangeRehydratesEvictedPages(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.selectNode(-1, false)
	last := tr.messages()[len(tr.messages())-1]
	tr.selection.anchor = testSelectionPoint(last.Turn, 0, last.Nodes[0])
	tr.selection.focus = tr.selection.anchor
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
		t.Fatalf("retained %d payload pages", len(tr.pages))
	}
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("selection endpoints were lost")
	}
	text, err := selectionText(plan, transcriptPageSize, func(at aria.Anchor, limit int) (aria.Page, error) {
		return readBeforeAt(history, at, limit), nil
	})
	if err != nil || !strings.Contains(text, "message-001") || !strings.Contains(text, "message-120") {
		t.Fatalf("long range copy = %q, %v", text, err)
	}
}
