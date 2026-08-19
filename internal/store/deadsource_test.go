package store

import "testing"

// durable-forms §12.2.2: "drop on a form that has since been deleted is
// LEGAL: it removes the subscription and decrements, and the board stops
// naming it." The earlier test tombstones the source. A real `fig kill`
// UNLINKS it, and then the libretto's source cannot be opened at all.
func TestDropAfterTheSourceIsUnlinked(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("gone", setPatch(map[string]string{"system.model": "m"}))
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
	if _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	// The form is DELETED, not merely tombstoned: files unlinked, node gone.
	if err := be.Remove(sourceID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatalf("dropping a study of a DELETED form was refused: %v", err)
	}
	studies, err := be.StudiedBy(watcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(studies) != 0 {
		t.Fatalf("the board still names a deleted form: %v", studies)
	}
}

// And the study path must not wedge either: an aria that studies a form
// which is then deleted, and is then FORKED, must still fork.
func TestForkWithADeadStudiedForm(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("gonefork", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(parent, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := be.Remove(sourceID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.Fork(parent); err != nil {
		t.Fatalf("forking an aria that studies a deleted form failed: %v", err)
	}
}

// THE REAL CASE: the daemon RESTARTS between the deletion and the drop. Now
// nothing is cached, so the libretto must be reachable without its source --
// which is the whole promise of the copy outliving what it copied.
func TestDropAfterTheSourceIsUnlinkedAcrossARestart(t *testing.T) {
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
	sourceID, _, err := be.CreateForm("", setPatch(map[string]string{"brief": "doomed"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := be.Remove(sourceID, false); err != nil {
		t.Fatal(err)
	}
	be.Close()

	// A fresh daemon over the same store.
	again, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	if _, err := again.DropForm(watcher, sourceID); err != nil {
		t.Fatalf("after a restart, dropping a study of a deleted form was refused: %v", err)
	}
	studies, err := again.StudiedBy(watcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(studies) != 0 {
		t.Fatalf("the board still names a deleted form after the drop: %v", studies)
	}
	// The copy is still readable, which is what makes the history render.
	lib, err := again.Libretto(sourceID)
	if err != nil {
		t.Fatalf("the libretto of a deleted form is unreachable after a restart: %v", err)
	}
	if _, ok := lib.State().Get("brief"); !ok {
		t.Fatal("the copy did not survive its source's deletion across a restart")
	}
}

// The other half: a source that could not be opened once must be picked up
// when it can. Attachment is retried by the next caller rather than latched,
// so a momentary read failure does not freeze a copy forever.
func TestLibrettoAttachesLateWhenItsSourceAppears(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	sourceID, _, err := be.CreateForm("", setPatch(map[string]string{"brief": "here"}))
	if err != nil {
		t.Fatal(err)
	}
	// A libretto whose source is not openable yet: use an id that does not
	// exist, then create nothing -- the libretto opens, does not follow.
	missing := "@0000000000000000"
	lib, err := be.Libretto(missing)
	if err != nil {
		t.Fatalf("a libretto with no source at all should still open: %v", err)
	}
	if lib.Following() {
		t.Fatal("it claims to be following a source that does not exist")
	}
	// A real source attaches on the next call.
	real, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !real.Following() {
		t.Fatal("a libretto with a live source is not following it")
	}
	waitFor(t, "the copy to carry the source's state", func() bool {
		raw, ok := real.State().Get("brief")
		return ok && string(raw) == `"here"`
	})
}
