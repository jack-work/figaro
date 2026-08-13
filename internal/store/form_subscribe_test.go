package store

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// The race the ordering exists to close: a patch landing between registering
// and reading the snapshot must be in BOTH, never in neither. Hammered, with
// the reader reconstructing state from snapshot plus stream and comparing it
// to the writer's own.
func TestSubscribeFromHasNoGap(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		f := NewMemForm()

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = f.Apply(kv("k", fmt.Sprint(i)), 0)
			}
		}()

		time.Sleep(time.Duration(attempt%7) * 100 * time.Microsecond)
		sub := f.SubscribeFrom(4096)

		time.Sleep(time.Millisecond)
		close(stop)
		wg.Wait()

		final, finalV := f.Snapshot()

		// Fold the stream onto the snapshot, dropping duplicates by version.
		state, at := sub.Snap, sub.At
		for drained := false; !drained; {
			select {
			case ev := <-sub.C:
				if ev.Missed > 0 {
					t.Fatal("buffer was ample; a resync means the test is wrong")
				}
				if ev.Version <= at {
					continue // a duplicate, which is the point
				}
				state = state.Apply(ev.Applied)
				at = ev.Version
			default:
				drained = true
			}
		}
		if at != finalV {
			t.Fatalf("attempt %d: stream ended at %d, form at %d: a gap", attempt, at, finalV)
		}
		got, _ := state.Get("k")
		want, _ := final.Get("k")
		if string(got) != string(want) {
			t.Fatalf("attempt %d: reconstructed %s, form holds %s", attempt, got, want)
		}
		sub.Close()
		f.Close()
	}
}

// A subscriber that stops reading must not stop the writer, and must be told
// it fell behind rather than silently losing patches.
func TestSlowSubscriberResyncsAndDoesNotBlock(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	sub := f.SubscribeFrom(2)
	defer sub.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if _, err := f.Apply(kv("k", fmt.Sprint(i)), 0); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a subscriber that stopped reading blocked the writer")
	}

	if sub.Missed() == 0 {
		t.Fatal("events were dropped without recording that they were")
	}
	// Draining makes room, and the next event carries the marker in band.
	for drained := false; !drained; {
		select {
		case <-sub.C:
		default:
			drained = true
		}
	}
	if _, err := f.Apply(kv("k", "after"), 0); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.C:
		if ev.Missed == 0 {
			t.Fatal("a subscriber that fell behind was not told so in band")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after draining")
	}
}

func TestSubscribeCloseIsIdempotent(t *testing.T) {
	f := NewMemForm()
	defer f.Close()
	s := f.SubscribeFrom(4)
	s.Close()
	s.Close()
	if _, err := f.Apply(kv("a", "1"), 0); err != nil {
		t.Fatalf("closing a subscription disturbed the writer: %v", err)
	}
}

// Through the backend, which is how everything that is not a test reaches a
// form: the same no-gap guarantee, and the aria's own board is subscribable
// like any other.
func TestSubscribeThroughBackend(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	id, _, err := be.CreateForm("", patchSet(map[string]string{"seed": "0"}))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := be.SubscribeForm(id, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if _, err := be.ApplyForm(id, patchSet(map[string]string{"brief": "moved"})); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.C:
		if ev.Version <= sub.At {
			t.Fatalf("event at %d is not past the snapshot at %d", ev.Version, sub.At)
		}
		if _, ok := ev.Applied.Set["brief"]; !ok {
			t.Fatalf("the event does not carry the patch: %v", ev.Applied)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event reached the subscriber")
	}
}
