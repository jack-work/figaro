package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A DAEMON THAT NEVER LOWERS THE FLOOR MUST NOT PIN A CORE.
//
// prefetchTranscriptPages loops: read a page, apply it, ask whether another is
// wanted, repeat. Every exit is a fact about the answer -- an empty page IS the
// floor, an error stops it -- so the loop rests on the page being PROGRESS. A
// read that keeps handing back what the pager already holds (an inclusive
// boundary, a store that will not go lower, a daemon that answers the same
// page for the same anchor) satisfies none of the exits: not empty, not an
// error, and `more` still true. The loop then reads forever, taking the render
// lock on every pass -- which is a pinned core, a pager that will not answer a
// key, and a pane that does not repaint on resize.
func TestPrefetchStopsWhenAPageMakesNoProgress(t *testing.T) {
	reader := &stuckReader{page: transcriptHistory(120)}
	in := newSearchInteractiveInput(reader, newRecordingTerminal().searchInputTerminal)

	in.mu.Lock()
	in.lt.tr.offset = 0 // scrolled to the top: the pager wants older history
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
	// And it must not have burned the machine getting there.
	if n := reader.calls(); n > 32 {
		t.Fatalf("prefetch made %d reads against a stuck floor", n)
	}
}

// stuckReader answers every backward read with the SAME page, and insists
// there is more before it: the shape of a boundary that never advances.
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

func (r *stuckReader) Read(context.Context, int) (aria.Page, error) { return aria.Page{}, nil }

func (r *stuckReader) ReadBefore(_ context.Context, at aria.Anchor, limit int) (aria.Page, error) {
	r.mu.Lock()
	r.n++
	n := r.n
	if len(r.asked) < 6 {
		r.asked = append(r.asked, at)
	}
	r.mu.Unlock()
	if n > 500 { // a runaway test must not run the machine out of memory
		return aria.Page{}, nil
	}
	// The tail, always, whatever was asked for -- and always "there is more".
	page := readBefore(r.page, recentCursor, limit)
	page.More.Before = true
	return page, nil
}

func (r *stuckReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

// A SEARCH THAT NEVER MATCHES MUST NOT WALK FOREVER. The search worker asks
// for a page, applies it, and asks again while the query is still live; the
// only thing that stops it is the pager admitting it is at the floor. Against
// a read that answers the same page and says "there is more before it", none
// of the exits fire -- and the worker takes the render lock on every pass,
// which is what a pinned core and a dead keyboard look like from the outside.
func TestSearchWorkerStopsAgainstAStuckFloor(t *testing.T) {
	reader := &stuckReader{page: transcriptHistory(120)}
	in := newSearchInteractiveInput(reader, newRecordingTerminal().searchInputTerminal)

	const query = "nothingmatchesthis"
	in.mu.Lock()
	in.lt.tr.offset = 0
	// The state a search leaves behind: a live query walking history.
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
	// THE COUNT IS THE ASSERTION. The worker used to ask for the same anchor
	// until the stub's own safety valve stopped it at 500 -- one RPC and two
	// acquisitions of the render lock per pass -- because buildIndex reset the
	// window to the tail between every ask (see transcript_index.go). A walk
	// that makes progress needs a handful of reads, not hundreds.
	if n := reader.calls(); n > 8 {
		t.Fatalf("the search walked history in %d reads, asking for %+v: it is fighting the tail",
			n, reader.asked)
	}
}

// THE SPIN GLUCK CAUGHT LIVE: one core pinned, no syscalls at all, every other
// thread parked on the render lock, and a bar reading "∴ · ✓ · <aria>" with NO
// page position on the rule -- which is the tell. No position means the
// retained window is smaller than the viewport, so wantOlder() is true; the
// window then grows and is reset to the tail on every pass:
//
//	growWindow      lowers the floor over history the store already holds
//	absorbOlder     → buildIndex → resetToTail() puts the floor back
//	wantOlder()     still true, because nothing moved
//	                → forever, inside the render lock, with no I/O to slow it
//
// A test with a deadline, because the failure mode is "does not return".
func TestPageCursorDoesNotSpinWhileFollowing(t *testing.T) {
	in := newSearchInteractiveInput(&stuckReader{page: transcriptHistory(120)},
		newRecordingTerminal().searchInputTerminal)

	in.mu.Lock()
	tr := in.lt.tr
	tr.follow = true // the live tail: what a pager does by default
	tr.tailWant = 0  // cold, so tailKeep() is the minimum page
	tr.search, tr.jump = nil, nil
	// A frame lands: buildIndex resets the window to the tail, which is SIX
	// messages while the store holds thirty. The floor is now well above what
	// is held -- which is what growWindow exists to fix, and what resetToTail
	// exists to undo.
	tr.buildIndex()
	tr.offset = 0 // the viewport sits at the top of what is held
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
