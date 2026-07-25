package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// pageOnce drives one older-page fetch through the same path the input loop
// uses, serving from an in-memory history.
func pageOnce(t *testing.T, tr *transcript, history []aria.Committed, dir transcriptPageDirection) bool {
	t.Helper()
	if dir == pageOlder {
		tr.offset, tr.checkOlder = 0, true
	} else {
		tr.offset, tr.checkNewer = len(tr.lineLT), true
	}
	req, ok := tr.pageCursor()
	if !ok {
		return false
	}
	pageLimit := req.limit
	if pageLimit <= 0 {
		pageLimit = transcriptPageSize
	}
	messages := req.cached
	if len(messages) == 0 {
		if req.after != 0 {
			r, _ := readNextPage(req.after, req.watermark, pageLimit,
				func(before, limit int) (aria.AriaRead, error) { return readBefore(history, before, limit), nil })
			messages = committedMessages(r.Committed)
		} else {
			limit := pageLimit
			if req.expected.Count != 0 {
				limit = req.expected.Count
			}
			messages = committedMessages(readBefore(history, req.before, limit).Committed)
		}
	}
	tr.applyPage(req, messages)
	return true
}

// TestTranscriptEvictionKeepsRowsForRetainedPayloads pins the cache lifecycle
// contract: a page evicted from the render window but still held in the payload
// LRU keeps its rendered rows, so oscillating across a page boundary costs
// neither I/O nor a re-render.
func TestTranscriptEvictionKeepsRowsForRetainedPayloads(t *testing.T) {
	history := transcriptHistory(300)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 10), 50, 10, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.lines()

	// Fill the window, then evict by paging one more time.
	for range transcriptPageLimit {
		if !pageOnce(t, tr, history, pageOlder) {
			t.Fatal("expected an older page")
		}
		tr.lines()
	}
	evicted, ok := tr.newestLT() // the newest retained message before eviction
	if !ok {
		t.Fatal("no retained messages")
	}
	if len(tr.payloadLRU) == 0 {
		t.Fatal("eviction did not retain a payload for the return trip")
	}
	kept := 0
	for _, page := range tr.payloadLRU {
		for _, m := range page.messages {
			if _, ok := tr.rowCache[m.LT]; ok {
				kept++
			}
		}
	}
	if kept == 0 {
		t.Fatalf("evicted-but-retained payloads lost every cached row (newest was %d)", evicted)
	}

	// Turning around must not re-render anything: the payload comes from the
	// LRU and the rows are still cached.
	before := len(tr.rowCache)
	if !pageOnce(t, tr, history, pageNewer) {
		t.Fatal("expected a newer page")
	}
	misses := 0
	for _, m := range tr.messages() {
		if _, ok := tr.rowCache[m.LT]; !ok {
			misses++
		}
	}
	if misses != 0 {
		t.Fatalf("returning across the boundary re-rendered %d messages (rowCache was %d)", misses, before)
	}
}

// TestTranscriptCachesStayBounded pins that keeping rows for LRU payloads does
// not leak: the rowCache never exceeds the messages actually retained (window +
// payload LRU).
func TestTranscriptCachesStayBounded(t *testing.T) {
	history := transcriptHistory(600)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 10), 50, 10, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 12 {
		if !pageOnce(t, tr, history, pageOlder) {
			break
		}
		tr.lines()
	}
	for range 6 {
		if !pageOnce(t, tr, history, pageNewer) {
			break
		}
		tr.lines()
	}
	retained := map[int]bool{}
	for _, m := range tr.messages() {
		retained[m.LT] = true
	}
	for _, page := range tr.payloadLRU {
		for _, m := range page.messages {
			retained[m.LT] = true
		}
	}
	for lt := range tr.rowCache {
		if !retained[lt] {
			t.Fatalf("rowCache holds rows for unretained LT %d (cache=%d, retained=%d)",
				lt, len(tr.rowCache), len(retained))
		}
	}
	if max := transcriptPageSize * (transcriptPageLimit + transcriptPayloadLRULimit); len(tr.rowCache) > max {
		t.Fatalf("rowCache grew to %d entries, bound is %d", len(tr.rowCache), max)
	}
}
