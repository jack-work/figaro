package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// TestTranscript_ForwardSelectionRequestsEvictedPage is GONE. It pinned that
// dragging a selection past the bottom of the retained window asked for a
// FORWARD page, a direction that only existed because history lived in a
// second copy the window could slide off the tail. The window reaches the live
// tail by construction now, so there is nothing newer to request.

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
	// PINNED, NOT FROZEN. Scrolling away keeps the open message in the window -
	// it is the last thing in the store's tail interval, and it keeps arriving
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
// beside its frozen window as heldOpen; phase 2a then narrowed it further,
// because the first page of older history took the window off the tail. With
// the whole window an interval into the store both ends are in the same
// structure again, and the far end can be anything the store still holds.
func TestTranscript_LongRangeRehydratesEvictedPages(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
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
		if !pageOnce(tr, history) {
			t.Fatal("expected older page")
		}
	}
	first := tr.messages()[0]
	tr.selection.focus = testSelectionPoint(first.Turn, 0, first.Nodes[0])
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
