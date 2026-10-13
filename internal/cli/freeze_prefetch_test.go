package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// A read that returns a page the pager already holds (not empty, not an
// error, more still true) must end the prefetch loop, or it spins under the
// render lock.
func TestPrefetchStopsWhenAPageMakesNoProgress(t *testing.T) {
	reader := &stuckReader{page: transcriptHistory(120)}
	in := newSearchInteractiveInput(reader, newRecordingTerminal().searchInputTerminal)

	in.mu.Lock()
	in.lt.tr.offset = 0
	in.lt.tr.wantTop = true
	req, need := in.lt.transcriptPageCursor()
	in.mu.Unlock()
	if !need {
		t.Fatal("a pager at the top of its window should want a page")
	}

	done := make(chan struct{})
	go in.prefetchTranscriptPages(req, done)

	select {
	case <-done:
		t.Logf("prefetch stopped after %d reads", reader.calls())
	case <-time.After(3 * time.Second):
		t.Fatalf("prefetch is still reading after 3s (%d reads): it will spin forever",
			reader.calls())
	}
	if n := reader.calls(); n > 32 {
		t.Fatalf("prefetch made %d reads against a stuck floor", n)
	}
}

// stuckReader answers every backward read with the same page and more=true.
type stuckReader struct {
	mu    sync.Mutex
	n     int
	asked []aria.Anchor
	page  []aria.TurnPart
}

func (r *stuckReader) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func (r *stuckReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *stuckReader) ReadBefore(_ context.Context, at aria.Anchor, limit int) (aria.Page, error) {
	r.mu.Lock()
	r.n++
	n := r.n
	if len(r.asked) < 6 {
		r.asked = append(r.asked, at)
	}
	r.mu.Unlock()
	if n > 500 {
		return aria.Page{}, nil
	}
	page := readBefore(r.page, recentCursor, limit)
	page.More.Before = true
	return page, nil
}

func (r *stuckReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

// The search worker must stop against a read that never advances the floor.
func TestSearchWorkerStopsAgainstAStuckFloor(t *testing.T) {
	reader := &stuckReader{page: transcriptHistory(120)}
	in := newSearchInteractiveInput(reader, newRecordingTerminal().searchInputTerminal)

	const query = "nothingmatchesthis"
	in.mu.Lock()
	in.lt.tr.offset = 0
	in.lt.tr.search = &transcriptSearch{query: query}
	in.searchGen++
	gen := in.searchGen
	ctx, cancel := context.WithCancel(context.Background())
	in.searchCancel, in.searchQuery = cancel, query
	in.mu.Unlock()

	done := make(chan struct{})
	go in.pageTranscriptSearch(ctx, cancel, done, gen, query)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("the search worker is still reading after 3s (%d reads): it will spin forever",
			reader.calls())
	}
	// The count is the assertion: a walk that makes progress needs a handful
	// of reads.
	if n := reader.calls(); n > 8 {
		t.Fatalf("the search walked history in %d reads, asking for %+v: it is fighting the tail",
			n, reader.asked)
	}
}

// While following, growWindow must not lower a floor that resetToTail puts
// straight back; the failure mode is "does not return", hence the deadline.
func TestPageCursorDoesNotSpinWhileFollowing(t *testing.T) {
	in := newSearchInteractiveInput(&stuckReader{page: transcriptHistory(120)},
		newRecordingTerminal().searchInputTerminal)

	in.mu.Lock()
	tr := in.lt.tr
	tr.follow = true
	tr.tailWant = 0
	tr.search, tr.jump = nil, nil
	// The tail window holds six messages while the store holds thirty.
	tr.buildIndex()
	tr.offset = 0
	in.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		in.mu.Lock()
		defer in.mu.Unlock()
		in.lt.transcriptPageCursor()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pageCursor has not returned in 2s: it is growing the window and " +
			"having the floor reset under it, forever, with the render lock held")
	}
}
