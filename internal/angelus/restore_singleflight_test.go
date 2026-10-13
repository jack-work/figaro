package angelus

// A wake is a single flight: the entry exists only while it runs.

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRestoreSingleFlightLeavesNothingBehind(t *testing.T) {
	// No backend, so every wake fails: the case most likely to leak an entry.
	h := &handlers{angelus: &Angelus{Registry: NewRegistry()}}
	const arias = 64

	var wg sync.WaitGroup
	for i := 0; i < arias; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				_, _ = h.restoreByID(context.Background(), id, "test")
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
		_, err := h.restoreByID(ctx, "stuck", "test")
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
