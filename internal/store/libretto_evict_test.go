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

// The other half of the same rule: once nobody is streaming from it, the form
// is ordinary again and the sweep may have it. A lease that never expires is
// a leak with a justification.
func TestAnUnsubscribedFormIsEvictableAgain(t *testing.T) {
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
	if n := be.EvictIdle(map[string]bool{}, 0); n == 0 {
		t.Fatal("the sweep evicted nothing at all, so this proves nothing")
	}
	be.mu.Lock()
	_, stillResident := be.forms[sourceID]
	be.mu.Unlock()
	if !stillResident {
		t.Fatal("a form with a live subscriber was evicted")
	}

	// Drop the study: the fold stops, the subscription goes, the lease ends.
	if _, _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	be.EvictIdle(map[string]bool{}, 0)
	be.mu.Lock()
	_, residentAfter := be.forms[sourceID]
	be.mu.Unlock()
	if residentAfter {
		t.Fatal("a form nobody reads survived the sweep: the lease never expires")
	}
}
