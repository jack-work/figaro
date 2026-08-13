package store

import (
	"encoding/json"
	"testing"
	"time"
)

// After a daemon restart, does an EXISTING libretto follow its source again?
// Nothing in the boot path opens one: the sweep mints only what is MISSING,
// and resumeStudies declares the observed set without touching the copy.
func TestLibrettoFollowsAgainAfterARestart(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("restart", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, _, err := be.CreateForm("", setPatch(map[string]string{"brief": "before"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	be.Close()

	// A fresh daemon over the same store, as a restart gives.
	again, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	// The source moves while nobody has touched the libretto.
	if _, err := again.ApplyForm(sourceID, setPatch(map[string]string{"brief": "after"})); err != nil {
		t.Fatal(err)
	}
	lib, err := again.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, ok := lib.State().Get("brief")
		if ok && string(raw) == `"after"` {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	raw, _ := lib.State().Get("brief")
	var got string
	json.Unmarshal(raw, &got)
	t.Fatalf("after a restart the copy is stale: %q (the source says \"after\")", got)
}
