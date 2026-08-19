package store

import "testing"

// ONE WRITER PER FORM is the base rule. The sweep opened a libretto with
// OpenLibretto, which constructs a NEW Form over the same stump channel --
// so a libretto already open and following had a second writer appending to
// its log, each computing versions from its own replayed state.
func TestSweepDoesNotCreateASecondWriter(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("onewriter", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID) // the live instance, following
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs = %d before the sweep", got)
	}
	// Force a correction the sweep must make.
	if err := lib.setRefs(9); err != nil {
		t.Fatal(err)
	}
	if _, err := be.ReconcileLibrettos(); err != nil {
		t.Fatal(err)
	}
	// If the sweep wrote through a SECOND instance, the live one still says
	// nine: two writers, two published states, one log.
	if got := lib.Refs(); got != 1 {
		t.Fatalf("the live libretto says refs=%d after the sweep corrected it to 1: "+
			"the sweep wrote through a second writer", got)
	}
}

// Re-attaching a libretto that is already current must not write anything:
// the seed is the whole state, and a seed per boot per libretto is growth
// with no information in it.
func TestReattachingACurrentLibrettoWritesNothing(t *testing.T) {
	be, sourceID, src := librettoFixture(t)
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "steady"})); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the copy to catch up", func() bool {
		raw, ok := lib.State().Get("brief")
		return ok && string(raw) == `"steady"`
	})
	version := lib.Version()

	// Detach and re-attach, exactly as a restart or a late attach does.
	lib.Close()
	be.closeLibretto(sourceID)
	again, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Following() {
		t.Fatal("the re-opened libretto is not following")
	}
	if got := again.Version(); got != version {
		t.Fatalf("re-attaching wrote %d record(s): version %d -> %d",
			got-version, version, got)
	}
	// And it still follows: a new patch lands.
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "moved"})); err != nil {
		t.Fatal(err)
	}
	_ = src
	waitFor(t, "the re-attached copy to follow", func() bool {
		raw, ok := again.State().Get("brief")
		return ok && string(raw) == `"moved"`
	})
}
