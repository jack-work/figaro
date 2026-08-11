package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// THE CACHE LIFECYCLE, AFTER THE SECOND COPY WENT AWAY.
//
// This file used to pin the payload LRU: a page evicted from the render window
// kept its payload (and so its rendered rows) so the return trip cost neither
// I/O nor a re-render. There is no page cache any more: the STORE holds the
// messages and the window is an interval into it: so the same two claims are
// made against the store instead:
//
//  1. turning around costs NO READ and NO RE-RENDER, because the window grows
//     back over messages the one owner still holds;
//  2. the row cache never outlives the store's own retention.

// TestTurningAroundCostsNoReadAndNoRerender: page history in, go back to the
// tail (G, which raises the floor), then scroll up again. The second trip up
// must be served entirely out of the store: pageCursor returns no request at
// all, and every message it brings back must still have its rows.
func TestTurningAroundCostsNoReadAndNoRerender(t *testing.T) {
	history := transcriptHistory(300)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 10), 50, 10, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.lines()

	for range transcriptPageLimit {
		tr.offset = 0
		if !pageOnce(tr, history) {
			t.Fatal("expected an older page")
		}
		tr.lines()
	}
	deep := tr.messages()
	oldest := deep[0].Turn

	tr.key('G') // back to the tail: the floor rises, the store keeps the history
	tr.lines()
	if got := tr.messages()[0].Turn; got <= oldest {
		t.Fatalf("G did not shrink the window back to the tail (oldest %d)", got)
	}

	// Now turn around. Every page of this trip is already in the store, so the
	// pager must ask the WIRE for nothing.
	tr.follow = false
	for range transcriptPageLimit {
		tr.offset = 0
		if req, ok := tr.pageCursor(); ok {
			t.Fatalf("the return trip hit the wire: %+v", req)
		}
	}
	back := tr.messages()
	if back[0].Turn > oldest {
		t.Fatalf("the return trip did not reach turn %d (stopped at %d)", oldest, back[0].Turn)
	}
	misses := 0
	for _, m := range back {
		if _, ok := tr.rowCache[keyOf(m)]; !ok {
			misses++
		}
	}
	if misses != 0 {
		t.Fatalf("the return trip re-rendered %d of %d messages", misses, len(back))
	}
}

// TestRowCacheNeverOutlivesTheStore: rows follow the one owner, so the cache
// cannot hold a message the store has forgotten.
func TestRowCacheNeverOutlivesTheStore(t *testing.T) {
	history := transcriptHistory(600)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 10), 50, 10, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 12 {
		tr.offset = 0
		if !pageOnce(tr, history) {
			break
		}
		tr.lines()
	}
	tr.key('G') // re-attach: evictStale prunes what fell far behind the window
	tr.lines()

	retained := map[int]bool{}
	client.ForEachIn(aria.Anchor{}, windowEnd, func(m aria.Message) bool {
		retained[m.Turn] = true
		return true
	})
	if open := tr.openMessage(); open != nil {
		retained[open.Turn] = true
	}
	for k := range tr.rowCache {
		if !retained[k.turn()] {
			t.Fatalf("rowCache holds rows for turn %d, which the store has forgotten (cache=%d, retained=%d)",
				k.turn(), len(tr.rowCache), len(retained))
		}
	}
}
