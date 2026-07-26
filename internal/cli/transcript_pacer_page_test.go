package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
)

// ---------------------------------------------------------------------------
// Where B's pacer meets D's prefetch worker.
//
// D made history paging asynchronous: the fetch runs on its own goroutine and
// calls applyPage — and so render() — under the shared render mutex, off the
// input goroutine. B then put a 120 fps gate in front of render(). The two
// compose by design (a landing page should be paced like any other frame), but
// the composition has a failure mode neither axis could see alone: a page that
// lands inside the frame interval is *refused*, and nothing on the input
// goroutine is going to come along and redraw it. The user scrolled, stopped,
// and is now looking at a screen that does not contain the history they were
// waiting for.
//
// The invariant, therefore: a refused page landing must always be redeemed by
// the trailing render. These tests pin it on a fake clock (exactly), on a real
// clock end-to-end (through the actual prefetch worker), and under -race with
// the timer goroutine and the prefetch worker contending for the same mutex.
// ---------------------------------------------------------------------------

// pagedPacerTurn builds a pager on a fake clock with the pacer armed, plus a
// settle() that drains any owed trailing render so a test starts from a screen
// that is genuinely up to date.
func pagedPacerTurn(t *testing.T, w *countingWriter) (lt *livelogTurn, mu *sync.Mutex, now *time.Time, timers *[]func(), settle func()) {
	t.Helper()
	var lock sync.Mutex
	lt = newLivelogTurn(w, 80, 24, &renderSettings{}, "aria0001", time.Unix(0, 0), nil, nil, nil)
	clock := time.Unix(3000, 0)
	var armed []func()
	lt.pace.now = func() time.Time { return clock }
	lt.pace.after = func(_ time.Duration, fn func()) { armed = append(armed, fn) }
	lt.setRenderLock(&lock)
	lt.enterTranscript()
	lt.apply(readBefore(transcriptHistory(120), recentCursor, transcriptPageSize))

	// settle drains any owed trailing render. Call it WITHOUT the render lock
	// held: the trailing render takes it, exactly as the real timer goroutine
	// does.
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

// assertFrameIsFresh is the anti-staleness oracle: what the terminal is holding
// must equal what composing the current state right now would produce. It is
// the property a dropped repaint violates, and unlike "did a write happen" it
// does not care how many frames it took to get there.
func assertFrameIsFresh(t *testing.T, tr *transcript, what string) {
	t.Helper()
	shown := append([]string(nil), tr.prev...)
	tr.renderFrame()
	if !equalStrings(shown, tr.prev) {
		t.Fatalf("%s: the screen is stale: %s", what, firstDiff(shown, tr.prev))
	}
}

// TestPacedPageLanding_IsNeverSwallowed is the invariant, stated exactly. A
// page that lands inside the frame interval is refused — and then painted by
// the trailing render, leaving the screen up to date.
func TestPacedPageLanding_IsNeverSwallowed(t *testing.T) {
	var w countingWriter
	lt, mu, now, timers, settle := pagedPacerTurn(t, &w)

	// Scroll to the top, which arms an older fetch; settle so the pacer owes
	// nothing and the next refusal is unambiguously the page's.
	mu.Lock()
	lt.transcriptDispatch(keyEvent{b: 'g'})
	lt.transcriptDispatch(keyEvent{b: 'g'})
	req, need := lt.transcriptPageCursor()
	mu.Unlock()
	settle()
	if !need {
		t.Fatal("gg at the top of the retained window did not ask for older history")
	}

	// The page lands 200 µs later — well inside the 1/120 s frame interval, so
	// the gate refuses it.
	*now = now.Add(200 * time.Microsecond)
	w.reset()
	mu.Lock()
	before, _ := lt.tr.oldestLT()
	lt.transcriptApplyPage(req, committedMessages(readBefore(transcriptHistory(120), req.before, req.limit)))
	after, _ := lt.tr.oldestLT()
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

	// Redeem it.
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

// TestPacedPageLanding_NoFurtherInputRequired is the same invariant end to end,
// on a real clock and through D's actual prefetch worker: nothing touches the
// input loop after the scroll, and the screen still has to catch up.
func TestPacedPageLanding_NoFurtherInputRequired(t *testing.T) {
	reader := &slowHistoryReader{history: transcriptHistory(300), delay: 20 * time.Millisecond}
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	var mu sync.Mutex
	in.mu = &mu
	in.lt.setRenderLock(&mu) // real time.AfterFunc, real 120 fps ceiling

	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	defer func() {
		close(tc.reads)
		<-done
	}()

	mu.Lock()
	before, _ := in.lt.tr.oldestLT()
	mu.Unlock()

	tc.send([]byte("gg")) // one burst, then silence

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		oldest, _ := in.lt.tr.oldestLT()
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
	// Nothing has been sent since "gg"; whatever is on the glass is what the
	// trailing render left there.
	mu.Lock()
	defer mu.Unlock()
	assertFrameIsFresh(t, in.lt.tr, "after an unattended page landing")
}

// blockingHistoryReader answers every ReadBefore, holding the caller for a
// jittered spell so the prefetch worker and the pacer's timer goroutine are
// genuinely interleaved rather than politely taking turns.
type blockingHistoryReader struct {
	history []aria.TurnPart
	mu      sync.Mutex
	calls   int
}

func (r *blockingHistoryReader) Read(context.Context, int) (aria.Page, error) {
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

// TestPacerAndPrefetchDoNotDeadlock is the lock-order check, and is meant to be
// run under -race. Three parties take in.mu: the input goroutine (draining a
// batch), the prefetch worker (applying a landed page), and the pacer's timer
// goroutine (the trailing flush). There is exactly one mutex and no other lock
// in the graph — the pacer deliberately reuses the caller's — so this cannot
// deadlock by construction; the test is here because "by construction" is a
// claim, and because the race detector has opinions about who touches
// framePacer's fields.
func TestPacerAndPrefetchDoNotDeadlock(t *testing.T) {
	reader := &blockingHistoryReader{history: transcriptHistory(400)}
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	var mu sync.Mutex
	in.mu = &mu
	in.lt.setRenderLock(&mu)

	done := make(chan struct{})
	go func() { in.run(); close(done) }()

	// Hammer the pager for a while: scroll bursts up and down, each one racing
	// a landing page and a pending trailing render.
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

	// Let any armed trailing render fire and take the lock after the loop is
	// gone; it must not panic, block, or paint a torn frame.
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

// TestDeferredFrameStillClampsTheViewport is the regression for the bug this
// merge actually introduced (and the -race run above actually caught): a
// negative viewport offset.
//
// A/D let scrollBy overshoot and relied on render() clamping. B's gate made
// render() skippable, so a scroll burst past the top left offset negative, and
// the next off-frame reader — viewportAnchor, when a prefetched page lands —
// indexed t.lineTurn with it and panicked. The clamp now happens in front of the
// gate, so it survives batching and rate limiting alike.
func TestDeferredFrameStillClampsTheViewport(t *testing.T) {
	check := func(t *testing.T, tr *transcript, what string) {
		t.Helper()
		if tr.offset < 0 {
			t.Fatalf("%s: viewport offset is %d; every off-frame reader indexes with this", what, tr.offset)
		}
		// The reader that crashed. It must be callable at any moment, not only
		// just after a painted frame.
		tr.viewportAnchor()
	}

	t.Run("batched", func(t *testing.T) {
		tr := coalesceTranscript(t, &countingWriter{})
		tr.key('g')
		tr.key('g') // start at the top, so the flick has somewhere to overshoot
		tr.beginBatch()
		for range 50 {
			tr.scrollBy(-10) // a flick that runs off the top of the window
		}
		check(t, tr, "inside an open batch")
		tr.endBatch()
		check(t, tr, "after the batch closed")
	})

	t.Run("gated", func(t *testing.T) {
		tr := coalesceTranscript(t, &countingWriter{})
		tr.gate = func() bool { return false } // every frame refused
		for range 50 {
			tr.key('u') // half-page up
		}
		check(t, tr, "with every frame refused")
		tr.gate = nil
		tr.flush()
		check(t, tr, "after the trailing flush")
	})
}
