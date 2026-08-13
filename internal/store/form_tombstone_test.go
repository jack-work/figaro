package store

import (
	"testing"
)

// The death is a RECORD, so a subscriber hears it through the mechanism it
// already uses and a replay reproduces it. And it is final: the form takes
// no further patches.
func TestTombstoneIsARecordAndSeals(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	if _, err := f.Apply(kv("brief", "alive"), 0); err != nil {
		t.Fatal(err)
	}
	sub := f.SubscribeFrom(8)
	defer sub.Close()

	v, err := f.Tombstone("deleted by test")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tombstoned() {
		t.Fatal("a tombstoned form does not say so")
	}

	select {
	case ev := <-sub.C:
		if _, ok := ev.Applied.Set[TombstoneKey]; !ok {
			t.Fatalf("the death did not reach the subscriber as a patch: %v", ev.Applied)
		}
		if ev.Version != v {
			t.Fatalf("event at %d, tombstone at %d", ev.Version, v)
		}
	default:
		t.Fatal("no event: the death must ride the ordinary stream")
	}

	if _, err := f.Apply(kv("brief", "after"), 0); err == nil {
		t.Fatal("a sealed form accepted a patch")
	}
	snap, _ := f.Snapshot()
	if s, _ := snap.Get("brief"); string(s) != `"alive"` {
		t.Fatalf("a refused patch changed the state: %s", s)
	}
}

// A delete retried after a crash must not have to know whether it got there
// the first time.
func TestTombstoneIsIdempotent(t *testing.T) {
	f := NewMemForm()
	defer f.Close()
	v1, err := f.Tombstone("once")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := f.Tombstone("again")
	if err != nil {
		t.Fatalf("a second tombstone must be a no-op, not a failure: %v", err)
	}
	if v1 != v2 {
		t.Fatalf("the second tombstone moved the version: %d then %d", v1, v2)
	}
}

// Death survives a restart without anyone re-declaring it: the seal is
// rebuilt from the published state at open.
func TestTombstoneSurvivesReopen(t *testing.T) {
	log := &MemFormLog{}
	f, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Tombstone("gone"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	again, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !again.Tombstoned() {
		t.Fatal("a reopened form forgot it was dead")
	}
	if _, err := again.Apply(kv("k", "v"), 0); err == nil {
		t.Fatal("a reopened tombstoned form accepted a patch")
	}
}
