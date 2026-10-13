package cli

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// A history read in flight across a subject switch carries the old aria's
// coordinates and must not fold into the new subject's store.

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
		s.after()
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
	stub.after = func() { atomic.AddUint64(&in.subjectGen, 1) }

	fetch := in.historyFetcher()
	page, err := fetch(t.Context(), aria.Anchor{Turn: 9}, 10)
	if err == nil {
		t.Fatal("the fetcher handed back a page read for the previous subject")
	}
	if len(page.Parts) != 0 {
		t.Fatalf("the fetcher returned %d parts with its refusal", len(page.Parts))
	}

	fetch = in.historyFetcher()
	stub.after = nil
	page, err = fetch(t.Context(), aria.Anchor{Turn: 9}, 10)
	if err != nil || len(page.Parts) != 3 {
		t.Fatalf("a live fetcher returned %d parts, err %v", len(page.Parts), err)
	}
}
