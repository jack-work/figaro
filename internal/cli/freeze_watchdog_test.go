package cli

import (
	"sync"
	"testing"
	"time"
)

// A contended render lock is not a frozen one: TryLock fails on mere
// contention, and the spinner alone takes the lock eleven times a second.
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
	go hammer(time.Second/11, 5*time.Millisecond)
	go hammer(time.Second/30, 2*time.Millisecond)

	// Sampled as the watchdog samples, with its threshold scaled to the test's
	// patience.
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
	if worst > 500*time.Millisecond {
		t.Fatalf("a busy render lock reported a single acquisition open for %s", worst)
	}
}

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
	// The stamp identifies the episode; the watchdog reports each stamp once.
	if _, again := l.heldFor(time.Now()); again != stamp {
		t.Fatalf("the same acquisition changed stamp: %d then %d", stamp, again)
	}
	l.Unlock()
	if held, stamp := l.heldFor(time.Now()); stamp != 0 || held != 0 {
		t.Fatalf("a free lock reports %s under stamp %d", held, stamp)
	}
	l.Lock()
	defer l.Unlock()
	if _, next := l.heldFor(time.Now()); next == stamp {
		t.Fatal("a fresh acquisition reused the previous episode's stamp")
	}
}
