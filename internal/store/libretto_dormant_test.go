package store

import "testing"

// What does a studied form cost when NOBODY is awake? The libretto is keyed
// by source, not by observer, so it keeps folding while any board names it --
// and a form with a subscriber is immune to the idle sweep, by design.
func TestStudiedFormStaysResidentWhileObserversSleep(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("sleepy", setPatch(map[string]string{"system.model": "m"}))
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
	// Everything idle: the observer's own caches go.
	be.EvictIdle(map[string]bool{}, 0)

	be.mu.Lock()
	_, observerResident := be.forms[watcher]
	_, sourceResident := be.forms[sourceID]
	librettos := len(be.librettos)
	be.mu.Unlock()
	t.Logf("after evicting everything: observer resident=%v  source resident=%v  librettos=%d",
		observerResident, sourceResident, librettos)
	if observerResident {
		t.Fatal("the observer's own caches survived the sweep")
	}
	if !sourceResident {
		t.Fatal("the studied form was evicted out from under its libretto")
	}
	if librettos != 1 {
		t.Fatalf("librettos = %d while a board still studies", librettos)
	}

	// And the fold is still live: a patch to the source reaches the copy
	// with every observer asleep.
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "while asleep"})); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the copy to follow while everyone sleeps", func() bool {
		raw, ok := lib.State().Get("brief")
		return ok && string(raw) == `"while asleep"`
	})
}
