package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/rpc"
)

// slowHistoryReader serves history after a fixed delay, modelling a daemon RPC
// on a busy machine.
type slowHistoryReader struct {
	history []aria.Committed
	delay   time.Duration

	mu    sync.Mutex
	calls int
}

func (r *slowHistoryReader) Read(context.Context, int) (aria.AriaRead, error) {
	return aria.AriaRead{}, nil
}

func (r *slowHistoryReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func (r *slowHistoryReader) ReadBefore(ctx context.Context, before, limit int) (aria.AriaRead, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case <-time.After(r.delay):
	case <-ctx.Done():
		return aria.AriaRead{}, ctx.Err()
	}
	return readBefore(r.history, before, limit), nil
}

func (r *slowHistoryReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestTranscriptScrollDoesNotBlockOnHistoryFetch is the stall test. With a
// 750 ms history read outstanding, a burst of scroll keys must still be
// consumed promptly — the frames the user is scrolling through do not wait on
// the RPC — and the page must land once it arrives.
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
	oldestBefore, _ := in.lt.tr.oldestLT()
	in.mu.Unlock()

	// gg: jump to the top of the retained window, which arms an older fetch.
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

	// While that fetch is outstanding, a burst of scroll keys must be handled.
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

	// ... and the page still lands.
	landed := false
	for range 200 {
		in.mu.Lock()
		oldest, _ := in.lt.tr.oldestLT()
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

// TestTranscriptPrefetchArmsBeforeTheEdge pins the prefetch DISTANCE: the fetch
// is armed while the viewport is still a screen away from the top of the
// retained window, not once it has hit it.
func TestTranscriptPrefetchArmsBeforeTheEdge(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(transcriptHistory(300), recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.lines()

	// One full screen away from the top: nothing armed yet at the old distance.
	tr.offset = tr.h + tr.h/2
	tr.checkOlder = true
	if _, ok := tr.pageCursor(); !ok {
		t.Fatalf("no prefetch armed %d rows from the top (viewport %d)", tr.offset, tr.h)
	}

	// Far from the top: no fetch.
	tr.offset = transcriptPrefetchScreens*tr.h + 1
	tr.checkOlder = true
	if req, ok := tr.pageCursor(); ok {
		t.Fatalf("prefetched %d rows from the top: %+v", tr.offset, req)
	}
}
