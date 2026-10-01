package cli

import (
	"sync"
	"testing"
	"time"
)

// A BUSY LOCK IS NOT A FROZEN ONE, and the watchdog used to be unable to tell
// the difference: it polled TryLock, which fails on a merely contended mutex
// (no waiter queue, no spin, and an outright false while the mutex is in
// starvation mode). The pager takes the render lock eleven times a second for
// the spinner alone, so that reported a working session as a wedged one -- 236
// times in one log.
//
// The test is the duty cycle of a real pager, run past the point where the old
// watchdog had already cried freeze six times.
func TestRenderLockBusyIsNotStuck(t *testing.T) {
	var l renderLock
	stop := make(chan struct{})
	var wg sync.WaitGroup
	hammer := func(every, hold time.Duration) {
		defer wg.Done()
		tk := time.NewTicker(every)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				l.Lock()
				time.Sleep(hold)
				l.Unlock()
			}
		}
	}
	wg.Add(2)
	go hammer(time.Second/11, 5*time.Millisecond) // the clock, holding a frame
	go hammer(time.Second/30, 2*time.Millisecond) // keys and streamed tokens

	// Sampled the way the watchdog samples, but with the watchdog's threshold
	// scaled down to the test's patience: if a 5ms hold can look like 25 polls
	// of continuous possession, it can look like five seconds of it.
	deadline := time.Now().Add(2 * time.Second)
	worst := time.Duration(0)
	for time.Now().Before(deadline) {
		if held, stamp := l.heldFor(time.Now()); stamp != 0 && held > worst {
			worst = held
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	// Generous: the point is orders of magnitude, not a tight bound on a
	// scheduler. A false positive measures in seconds.
	if worst > 500*time.Millisecond {
		t.Fatalf("a busy render lock reported a single acquisition open for %s", worst)
	}
}

// And a holder that really does not let go is seen, once, with the duration it
// actually held for.
func TestRenderLockStuckIsSeenOnce(t *testing.T) {
	var l renderLock
	l.Lock()
	time.Sleep(30 * time.Millisecond)

	held, stamp := l.heldFor(time.Now())
	if stamp == 0 {
		t.Fatal("a held lock reports itself free")
	}
	if held < 30*time.Millisecond {
		t.Fatalf("held for %s, want at least 30ms", held)
	}
	// The stamp identifies the EPISODE: the watchdog reports a stamp once, so
	// a second poll of the same holder must carry the same stamp.
	if _, again := l.heldFor(time.Now()); again != stamp {
		t.Fatalf("the same acquisition changed stamp: %d then %d", stamp, again)
	}
	l.Unlock()
	if held, stamp := l.heldFor(time.Now()); stamp != 0 || held != 0 {
		t.Fatalf("a free lock reports %s under stamp %d", held, stamp)
	}
	// A new acquisition is a new episode, so the next freeze is reported
	// rather than suppressed by the last one's stamp.
	l.Lock()
	defer l.Unlock()
	if _, next := l.heldFor(time.Now()); next == stamp {
		t.Fatal("a fresh acquisition reused the previous episode's stamp")
	}
}
