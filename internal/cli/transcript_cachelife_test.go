package cli

import (
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Turning around costs no read and no re-render, and the row cache never
// outlives the store's retention.

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

	tr.key('G')
	tr.lines()
	if got := tr.messages()[0].Turn; got <= oldest {
		t.Fatalf("G did not shrink the window back to the tail (oldest %d)", got)
	}

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
	tr.key('G')
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
