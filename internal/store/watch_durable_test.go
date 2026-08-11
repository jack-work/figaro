package store

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// A durable watcher survives the idle sweep: eviction closes the Form and
// its plain OnCommit sinks with it, and a studied role is NEVER in the
// live set — so without re-arming, every study would go silent after the
// first sweep and nobody would know. The bug this test forbids is
// silence.
func TestWatchFormDurableSurvivesEviction(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	role, _, err := be.CreateForm("", message.Patch{Set: map[string]json.RawMessage{"name": json.RawMessage(`"role"`)}})
	if err != nil {
		t.Fatal(err)
	}

	var got atomic.Int64
	cancel, err := be.WatchFormDurable(role, "watcher-a", func(version uint64, patch message.Patch) {
		got.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := be.ApplyForm(role, message.Patch{Set: map[string]json.RawMessage{"a": json.RawMessage(`1`)}}); err != nil {
		t.Fatal(err)
	}
	if got.Load() != 1 {
		t.Fatalf("pre-eviction commits seen = %d, want 1", got.Load())
	}

	// The sweep: nothing is live, everything idle.
	if n := be.EvictIdle(map[string]bool{}, 0); n == 0 {
		t.Fatal("eviction evicted nothing; the premise is gone")
	}

	// The form reopens on the next write — the watcher must come back
	// with it.
	if _, err := be.ApplyForm(role, message.Patch{Set: map[string]json.RawMessage{"b": json.RawMessage(`2`)}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Load() != 2 {
		t.Fatalf("post-eviction commit lost: seen = %d, want 2 (the sink died with the evicted Form)", got.Load())
	}

	// Cancel, evict, write: the registration must NOT re-arm.
	cancel()
	if n := be.EvictIdle(map[string]bool{}, 0); n == 0 {
		t.Fatal("second eviction evicted nothing")
	}
	if _, err := be.ApplyForm(role, message.Patch{Set: map[string]json.RawMessage{"c": json.RawMessage(`3`)}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got.Load() != 2 {
		t.Fatalf("cancelled watcher re-armed: seen = %d, want 2", got.Load())
	}
}

// SINGULAR, not merely alive: N evict/reopen cycles and N same-owner
// re-registrations (an agent re-registers on every revival), then ONE
// write, must deliver EXACTLY once. If re-arm or re-register stacked
// copies, triplicate reminders would appear only on long-lived daemons —
// the kind of bug no fresh test process ever meets.
func TestWatchFormDurableDeliversExactlyOnce(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	role, _, err := be.CreateForm("", message.Patch{Set: map[string]json.RawMessage{"name": json.RawMessage(`"role"`)}})
	if err != nil {
		t.Fatal(err)
	}

	var got atomic.Int64
	sink := func(version uint64, patch message.Patch) { got.Add(1) }

	// Three "revivals": same owner, re-registered each time…
	for i := 0; i < 3; i++ {
		if _, err := be.WatchFormDurable(role, "aria-1", sink); err != nil {
			t.Fatal(err)
		}
		// …interleaved with evict/reopen cycles (reopen happens on the
		// write below; the eviction is what would stack re-arms).
		be.EvictIdle(map[string]bool{}, 0)
	}

	if _, err := be.ApplyForm(role, message.Patch{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let any duplicate arrive and convict itself
	if n := got.Load(); n != 1 {
		t.Fatalf("one write delivered %d times, want exactly 1", n)
	}
}
