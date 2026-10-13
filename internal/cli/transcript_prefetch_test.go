package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

type slowHistoryReader struct {
	history []aria.TurnPart
	delay   time.Duration

	mu    sync.Mutex
	calls int
}

func (r *slowHistoryReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *slowHistoryReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func (r *slowHistoryReader) ReadBefore(ctx context.Context, at aria.Anchor, limit int) (aria.Page, error) {
	before := int(at.Turn)
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case <-time.After(r.delay):
	case <-ctx.Done():
		return aria.Page{}, ctx.Err()
	}
	return readBefore(r.history, before, limit), nil
}

func (r *slowHistoryReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Scroll keys must be consumed while a history read is outstanding.
func TestTranscriptScrollDoesNotBlockOnHistoryFetch(t *testing.T) {
	const delay = 750 * time.Millisecond
	reader := &slowHistoryReader{history: transcriptHistory(300), delay: delay}
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	done := make(chan struct{})
	go func() {
		in.run()
		close(done)
	}()

	in.mu.Lock()
	oldestBefore := in.lt.tr.from.Turn
	in.mu.Unlock()

	// gg brings the viewport inside the prefetch distance of the floor.
	tc.send([]byte("gg"))
	deadline := time.Now().Add(delay / 2)
	for time.Now().After(deadline) == false {
		if reader.callCount() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if reader.callCount() == 0 {
		t.Fatal("scrolling to the top did not start a history fetch")
	}

	start := time.Now()
	for range 10 {
		tc.send([]byte{'j'})
	}
	offset := 0
	for time.Since(start) < delay/2 {
		in.mu.Lock()
		offset = in.lt.tr.offset
		in.mu.Unlock()
		if offset >= 10 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)
	if offset < 10 {
		t.Fatalf("input loop blocked on the history fetch: offset %d after %s", offset, elapsed)
	}
	if elapsed > delay/2 {
		t.Fatalf("scroll burst took %s with a %s fetch outstanding", elapsed, delay)
	}

	landed := false
	for range 200 {
		in.mu.Lock()
		oldest := in.lt.tr.from.Turn
		in.mu.Unlock()
		if oldest < oldestBefore {
			landed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !landed {
		t.Fatalf("prefetched page never landed (oldest still %d)", oldestBefore)
	}

	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}

func TestTranscriptPrefetchArmsBeforeTheEdge(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(transcriptHistory(300), recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.lines()

	tr.offset = tr.h + tr.h/2
	if _, ok := tr.pageCursor(); !ok {
		t.Fatalf("no prefetch armed %d rows from the top (viewport %d)", tr.offset, tr.h)
	}

	tr.offset = transcriptPrefetchScreens*tr.h + 1
	if req, ok := tr.pageCursor(); ok {
		t.Fatalf("prefetched %d rows from the top: %+v", tr.offset, req)
	}
}
