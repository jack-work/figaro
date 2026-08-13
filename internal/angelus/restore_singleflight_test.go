package angelus

// Waking a dormant aria is a SINGLE FLIGHT, not a lock table.
//
// The map this replaces handed out a *sync.Mutex per aria and never removed
// one: it grew by an entry per aria ever restored, for the life of the
// daemon. The entry here exists only while a wake is running, so the bound
// is concurrent wakes rather than arias ever woken (plans/lock-audit.md).

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRestoreSingleFlightLeavesNothingBehind(t *testing.T) {
	// A minimal daemon: a registry to consult and NO backend, so every wake
	// fails. A failing wake is the case most likely to leak an entry.
	h := &handlers{angelus: &Angelus{Registry: NewRegistry()}}
	const arias = 64

	var wg sync.WaitGroup
	for i := 0; i < arias; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				_, _ = h.restoreByID(context.Background(), id)
			}(id)
		}
	}
	wg.Wait()

	h.restoreMu.Lock()
	left := len(h.restoring)
	h.restoreMu.Unlock()
	if left != 0 {
		t.Fatalf("%d in-flight entries left after every wake returned: the map grows forever", left)
	}
}

// A caller whose context dies while another goroutine is mid-wake must not
// wait for it, and must not corrupt the entry the winner will delete.
func TestRestoreSingleFlightHonoursACancelledCaller(t *testing.T) {
	h := &handlers{
		angelus:   &Angelus{Registry: NewRegistry()},
		restoring: map[string]*restoreCall{},
	}
	call := &restoreCall{done: make(chan struct{})}
	h.restoring["stuck"] = call

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.restoreByID(ctx, "stuck")
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled caller returned success while the wake was still running")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled caller waited for someone else's wake")
	}
	close(call.done)
}
