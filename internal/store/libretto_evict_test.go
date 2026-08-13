package store

import (
	"encoding/json"
	"testing"
	"time"
)

// If the idle sweep evicts a form somebody is STUDYING, does the libretto
// keep following it? Nothing in the sweep asks.
func TestLibrettoSurvivesEvictionOfItsSource(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("evict", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "before"})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first patch to land", func() bool {
		raw, ok := lib.State().Get("brief")
		return ok && string(raw) == `"before"`
	})

	// THE SWEEP: everything idle, which includes the studied form.
	be.EvictIdle(map[string]bool{}, 0)

	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "after"})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, ok := lib.State().Get("brief")
		if ok && string(raw) == `"after"` {
			return // still following
		}
		time.Sleep(5 * time.Millisecond)
	}
	raw, _ := lib.State().Get("brief")
	var got string
	json.Unmarshal(raw, &got)
	t.Fatalf("the libretto stopped following when its source was evicted: copy says %q", got)
}

// THE COST OF NOT TEARING DOWN, asserted rather than left to be discovered.
//
// A libretto is never closed on refs==0 -- transferring a refcount across an
// instance boundary cannot be made safe, see study.go -- so the copy keeps
// following after the last drop, and a form with a subscriber is not idle.
// The consequence is that a form STUDIED ONCE stays resident for the life of
// the daemon, bounded by the number of forms studied since it started.
//
// If that ever needs to change, the change is not "close it on drop": it is
// a teardown that runs where no verb is in flight.
func TestADroppedStudyKeepsItsSourceResident(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("evict2", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if n := be.EvictIdle(map[string]bool{}, 0); n == 0 {
		t.Fatal("the sweep evicted nothing at all, so this proves nothing")
	}
	be.mu.Lock()
	_, sourceResident := be.forms[sourceID]
	librettos := len(be.librettos)
	be.mu.Unlock()
	if !sourceResident {
		t.Fatal("the source was evicted while its copy still follows it")
	}
	if librettos != 1 {
		t.Fatalf("librettos = %d after the last drop, want 1 (it is not torn down)", librettos)
	}
	// And it is still following, which is the point of keeping it: a study
	// arriving later finds a current copy rather than a stale one.
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "after the drop"})); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the copy to follow after the last drop", func() bool {
		raw, ok := lib.State().Get("brief")
		return ok && string(raw) == `"after the drop"`
	})
}
