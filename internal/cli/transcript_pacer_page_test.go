package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// A page lands from the prefetch goroutine under the render mutex; the frame
// gate may refuse it, and nothing on the input goroutine will redraw it, so a
// refused landing must always be redeemed by the trailing render.

func pagedPacerTurn(t *testing.T, w *countingWriter) (lt *livelogTurn, mu *renderLock, now *time.Time, timers *[]func(), settle func()) {
	t.Helper()
	var lock renderLock
	lt = newLivelogTurn(w, 80, 24, &renderSettings{}, "aria0001", time.Unix(0, 0), nil, nil, nil)
	clock := time.Unix(3000, 0)
	var armed []func()
	lt.pace.now = func() time.Time { return clock }
	lt.pace.after = func(_ time.Duration, fn func()) { armed = append(armed, fn) }
	lt.setRenderLock(&lock)
	lt.enterTranscript()
	tail := readBefore(transcriptHistory(120), recentCursor, transcriptPageSize)
	lt.apply(tail)
	lt.setMoreBefore(tail.More.Before)

	// settle takes the render lock itself: call it without the lock held.
	settle = func() {
		t.Helper()
		for range 10 {
			lock.Lock()
			clean := !lt.tr.dirty
			lock.Unlock()
			if len(armed) == 0 && clean {
				return
			}
			clock = clock.Add(transcriptFrameInterval)
			fns := armed
			armed = nil
			if len(fns) == 0 {
				lock.Lock()
				lt.tr.flush()
				lock.Unlock()
				continue
			}
			for _, fn := range fns {
				fn()
			}
		}
		t.Fatal("the pager never settled")
	}
	settle()
	w.reset()
	return lt, &lock, &clock, &armed, settle
}

// The terminal must hold what composing the current state would produce.
func assertFrameIsFresh(t *testing.T, tr *transcript, what string) {
	t.Helper()
	shown := append([]string(nil), tr.prev...)
	tr.renderFrame()
	if !equalStrings(shown, tr.prev) {
		t.Fatalf("%s: the screen is stale: %s", what, firstDiff(shown, tr.prev))
	}
}

func TestPacedPageLanding_IsNeverSwallowed(t *testing.T) {
	var w countingWriter
	lt, mu, now, timers, settle := pagedPacerTurn(t, &w)

	// Settle so the next refusal is unambiguously the page's.
	mu.Lock()
	lt.transcriptDispatch(keyEvent{b: 'g'})
	lt.transcriptDispatch(keyEvent{b: 'g'})
	req, need := lt.transcriptPageCursor()
	mu.Unlock()
	settle()
	if !need {
		t.Fatal("gg at the top of the retained window did not ask for older history")
	}

	// 200 µs is inside the 1/120 s frame interval.
	*now = now.Add(200 * time.Microsecond)
	w.reset()
	mu.Lock()
	before := lt.tr.from.Turn
	lt.transcriptApplyPage(req, (readBeforeAt(transcriptHistory(120),
		req.at, req.limit)))
	after := lt.tr.from.Turn
	dirty := lt.tr.dirty
	mu.Unlock()

	if after >= before {
		t.Fatalf("the page did not land: oldest LT %d -> %d", before, after)
	}
	if got := w.writes.Load(); got != 0 {
		t.Fatalf("a page landing inside the frame interval painted %d frames; the gate is not in play, so this test proves nothing", got)
	}
	if !dirty {
		t.Fatal("a refused page landing left the screen clean: the repaint is lost forever")
	}
	if len(*timers) != 1 {
		t.Fatalf("a refused page landing armed %d trailing renders, want exactly 1", len(*timers))
	}

	*now = now.Add(transcriptFrameInterval)
	(*timers)[0]()
	if got := w.writes.Load(); got != 1 {
		t.Fatalf("the trailing render painted %d frames, want 1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if lt.tr.dirty {
		t.Fatal("the trailing render left the screen dirty")
	}
	assertFrameIsFresh(t, lt.tr, "after a paced page landing")
}

// Real clock, real prefetch worker, nothing touches the input loop after gg.
func TestPacedPageLanding_NoFurtherInputRequired(t *testing.T) {
	reader := &slowHistoryReader{history: transcriptHistory(300), delay: 20 * time.Millisecond}
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	var mu renderLock
	in.mu = &mu
	in.lt.setRenderLock(&mu)

	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	defer func() {
		close(tc.reads)
		<-done
	}()

	mu.Lock()
	before := in.lt.tr.from.Turn
	mu.Unlock()

	tc.send([]byte("gg"))

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		oldest := in.lt.tr.from.Turn
		landed, dirty := oldest < before, in.lt.tr.dirty
		mu.Unlock()
		if landed && !dirty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pager never caught up with the landed page: oldest %d -> %d, dirty=%v", before, oldest, dirty)
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	assertFrameIsFresh(t, in.lt.tr, "after an unattended page landing")
}

// A jittered delay interleaves the prefetch worker and the timer goroutine.
type blockingHistoryReader struct {
	history []aria.TurnPart
	mu      sync.Mutex
	calls   int
}

func (r *blockingHistoryReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *blockingHistoryReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func (r *blockingHistoryReader) ReadBefore(ctx context.Context, at aria.Anchor, limit int) (aria.Page, error) {
	before := int(at.Turn)
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	select {
	case <-time.After(time.Duration(n%3) * time.Millisecond):
	case <-ctx.Done():
		return aria.Page{}, ctx.Err()
	}
	return readBefore(r.history, before, limit), nil
}

// Run under -race: input goroutine, prefetch worker and the pacer's timer
// goroutine all take the one mutex.
func TestPacerAndPrefetchDoNotDeadlock(t *testing.T) {
	reader := &blockingHistoryReader{history: transcriptHistory(400)}
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	var mu renderLock
	in.mu = &mu
	in.lt.setRenderLock(&mu)

	done := make(chan struct{})
	go func() { in.run(); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		if i%2 == 0 {
			tc.send([]byte(strings.Repeat("k", 12)))
		} else {
			tc.send([]byte(strings.Repeat("j", 5) + "gg"))
		}
		time.Sleep(time.Millisecond)
	}
	close(tc.reads)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the input loop never returned: pacer/prefetch deadlock")
	}

	// A trailing render firing after the loop is gone must not panic or block.
	time.Sleep(4 * transcriptFrameInterval)
	locked := make(chan struct{})
	go func() {
		mu.Lock()
		in.lt.tr.flush()
		mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("could not take the render lock after the input loop exited")
	}
	if reader.calls == 0 {
		t.Fatal("no history was ever fetched; the test never exercised the interleaving")
	}
}

// scrollBy may overshoot and render() is skippable, so the clamp must sit in
// front of the gate: viewportAnchor indexes t.lineTurn with the offset.
func TestDeferredFrameStillClampsTheViewport(t *testing.T) {
	check := func(t *testing.T, tr *transcript, what string) {
		t.Helper()
		if tr.offset < 0 {
			t.Fatalf("%s: viewport offset is %d; every off-frame reader indexes with this", what, tr.offset)
		}
		tr.viewportAnchor()
	}

	t.Run("batched", func(t *testing.T) {
		tr := coalesceTranscript(t, &countingWriter{})
		tr.key('g')
		tr.key('g')
		tr.beginBatch()
		for range 50 {
			tr.scrollBy(-10)
		}
		check(t, tr, "inside an open batch")
		tr.endBatch()
		check(t, tr, "after the batch closed")
	})

	t.Run("gated", func(t *testing.T) {
		tr := coalesceTranscript(t, &countingWriter{})
		tr.gate = func() bool { return false }
		for range 50 {
			tr.key('u')
		}
		check(t, tr, "with every frame refused")
		tr.gate = nil
		tr.flush()
		check(t, tr, "after the trailing flush")
	})
}
