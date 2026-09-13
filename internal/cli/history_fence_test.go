package cli

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A HOLE IS FILLED BY A WORKER THAT MAY OUTLIVE THE SUBJECT IT WAS FILLING IT
// FOR. The pager asks for the history under its window on a goroutine; a
// switch can land while that read is in flight, and the page it brings back
// carries the coordinates of the aria we have left. Folding it into the new
// subject's store puts one conversation's turns under another's turn ids,
// which is the whole reason the range store exists.
//
// This matters more since a switch RETAINS: the store it would land in is no
// longer empty, so the fabricated rows would sit in the middle of real ones.

type stubReader struct {
	pages int32
	after func()
}

func (s *stubReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (s *stubReader) ReadBefore(context.Context, aria.Anchor, int) (aria.Page, error) {
	atomic.AddInt32(&s.pages, 1)
	if s.after != nil {
		s.after() // the switch lands while this read is in flight
	}
	return aria.Page{Parts: partsFor(1, 3, "OLD")}, nil
}

func (s *stubReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func TestHistoryFetcher_RefusesAPageOwedToAnAriaWeHaveLeft(t *testing.T) {
	in := &interactiveInput{}
	stub := &stubReader{}
	in.fcli = stub
	// The switch happens while the read is in flight, which is the only
	// ordering that can corrupt anything.
	stub.after = func() { atomic.AddUint64(&in.subjectGen, 1) }

	fetch := in.historyFetcher()
	page, err := fetch(t.Context(), aria.Anchor{Turn: 9}, 10)
	if err == nil {
		t.Fatal("the fetcher handed back a page read for the previous subject")
	}
	if len(page.Parts) != 0 {
		t.Fatalf("the fetcher returned %d parts with its refusal", len(page.Parts))
	}

	// And a fetcher wired for the CURRENT subject still works, or the pager
	// can never fill a hole at all.
	fetch = in.historyFetcher()
	stub.after = nil
	page, err = fetch(t.Context(), aria.Anchor{Turn: 9}, 10)
	if err != nil || len(page.Parts) != 3 {
		t.Fatalf("a live fetcher returned %d parts, err %v", len(page.Parts), err)
	}
}
