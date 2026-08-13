package store

import (
	"encoding/json"
	"testing"
)

// The ordering is the design (durable-forms §12.2.1): every crash must leave
// the refcount too HIGH, because too high is a leak the sweep repairs and too
// low reclaims a copy a live observer still needs. These hold the
// POSTCONDITIONS of both verbs and the idempotence that makes a retry safe.

func TestStudyRetainsBeforeItDeclares(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("study", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcherA, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	watcherB, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "watch me"})); err != nil {
		t.Fatal(err)
	}

	if err := be.StudyForm(watcherA, sourceID); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs after one study = %d, want 1", got)
	}
	studies, err := be.StudiedBy(watcherA)
	if err != nil {
		t.Fatal(err)
	}
	if len(studies) != 1 || studies[0] != sourceID {
		t.Fatalf("board declares %v, want [%s]", studies, sourceID)
	}
	// The libretto is following: the copy carries the source's state.
	waitFor(t, "the libretto to hold the source's state", func() bool {
		raw, ok := lib.State().Get("brief")
		if !ok {
			return false
		}
		var got string
		json.Unmarshal(raw, &got)
		return got == "watch me"
	})

	// Studying twice is not two references: the board is a SET, and the
	// count is derived from the boards.
	if err := be.StudyForm(watcherA, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs after studying twice = %d, want 1", got)
	}

	// A second observer shares the one libretto.
	if err := be.StudyForm(watcherB, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 2 {
		t.Fatalf("refs after a second observer = %d, want 2", got)
	}
	other, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if other != lib {
		t.Fatal("two observers got two libretto instances; the fold is per libretto, not per observer")
	}

	// And the sweep agrees with the verb, which is the invariant the two of
	// them exist to keep.
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed with the verb: %+v", audit)
	}
}

func TestDropDeclaresBeforeItReleases(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("drop", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	studies, err := be.StudiedBy(watcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(studies) != 0 {
		t.Fatalf("board still declares %v after a drop", studies)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if got := lib.Refs(); got != 0 {
		t.Fatalf("refs after the only observer dropped = %d, want 0", got)
	}
	// The COPY survives: an IR record references this libretto forever, so
	// dropping the last observer must not take the state with it.
	if _, ok := lib.State().Get(KeyLibrettoAlive); !ok {
		t.Fatal("the libretto lost its state when the last observer dropped")
	}
	// Dropping again is legal and changes nothing.
	if err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatalf("a second drop errored: %v", err)
	}
	if got := lib.Refs(); got != 0 {
		t.Fatalf("a second drop moved the count to %d", got)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 || audit.Orphaned != 1 {
		t.Fatalf("after the last drop the sweep says %+v; want orphaned=1, corrected=0", audit)
	}
}

// Dropping a form that has since been deleted is legal (§12.2.2): the
// subscription goes, the board stops naming it, and the copy stays.
func TestDropAfterTheSourceIsDead(t *testing.T) {
	be, sourceID, src := librettoFixture(t)
	outfit, err := be.CreateOutfit("dead", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "doomed"})); err != nil {
		t.Fatal(err)
	}
	if err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Tombstone("deleted while studied"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the death to reach the libretto", func() bool { return !lib.Alive() })
	if err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatalf("dropping a dead form was refused: %v", err)
	}
	again, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	raw, ok := again.State().Get("brief")
	if !ok {
		t.Fatal("the copy died with the source; history can no longer render it")
	}
	var got string
	json.Unmarshal(raw, &got)
	if got != "doomed" {
		t.Fatalf("the copy says %q", got)
	}
}
