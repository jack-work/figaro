package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
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

	if _, _, err := be.StudyForm(watcherA, sourceID); err != nil {
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
	if _, _, err := be.StudyForm(watcherA, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs after studying twice = %d, want 1", got)
	}

	// A second observer shares the one libretto.
	if _, _, err := be.StudyForm(watcherB, sourceID); err != nil {
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
	if _, _, err := be.StudyForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.DropForm(watcher, sourceID); err != nil {
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
	if _, _, err := be.DropForm(watcher, sourceID); err != nil {
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
	if _, _, err := be.StudyForm(watcher, sourceID); err != nil {
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
	if _, _, err := be.DropForm(watcher, sourceID); err != nil {
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

// FORK is a refcount participant (durable-forms §12.2.2), and it is the one
// that fails in the unrecoverable direction: a child inherits the board,
// therefore the study set, therefore every study its parent held -- with
// nothing incrementing the librettos it names. Fork, then let the parent
// drop, and refs reaches zero while the child is still observing.
func TestForkInheritsTheStudyAndItsReference(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("fork", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.StudyForm(parent, sourceID); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs before the fork = %d, want 1", got)
	}

	cont, alt, err := be.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 2 {
		t.Fatalf("refs after a fork = %d, want 2: the child studies it too", got)
	}
	// And the boards agree with the count, which is what the sweep checks.
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed after a fork: %+v", audit)
	}

	// The parent dropping must not reclaim what the child still observes.
	if _, _, err := be.DropForm(cont, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got == 0 {
		t.Fatal("one branch dropping took the reference the other still holds")
	}
	_ = alt
}

// KILL is the same rule at the other end: a board going out of existence
// stops studying what it named, or refs stays high forever.
func TestKillReleasesWhatItsBoardStudied(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("kill", setPatch(map[string]string{"system.model": "m"}))
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
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs before the kill = %d, want 1", got)
	}
	if err := be.Remove(watcher, false); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 0 {
		t.Fatalf("refs after killing the only observer = %d, want 0", got)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed after a kill: %+v", audit)
	}
}

// Every path that gives a board a COPY is a participant. ForkWith is the one
// a live `fig fork` takes, and it was missed while every test that called
// Fork passed -- which is why this asserts the property on both entry points
// rather than on the one the last test happened to use.
func TestEveryForkEntryPointInheritsTheReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		fork func(be *XwalBackend, parent string) error
	}{
		{"Fork", func(be *XwalBackend, parent string) error {
			_, _, err := be.Fork(parent)
			return err
		}},
		{"ForkWith", func(be *XwalBackend, parent string) error {
			_, _, err := be.ForkWith(parent, 0, setPatch(map[string]string{"note": "forked"}))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be, sourceID, _ := librettoFixture(t)
			outfit, err := be.CreateOutfit("forks", setPatch(map[string]string{"system.model": "m"}))
			if err != nil {
				t.Fatal(err)
			}
			parent, err := be.CreateConversation(outfit)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := be.StudyForm(parent, sourceID); err != nil {
				t.Fatal(err)
			}
			if err := tc.fork(be, parent); err != nil {
				t.Fatal(err)
			}
			lib, err := be.Libretto(sourceID)
			if err != nil {
				t.Fatal(err)
			}
			if got := lib.Refs(); got != 2 {
				t.Fatalf("%s left refs at %d, want 2", tc.name, got)
			}
			audit, err := be.ReconcileLibrettos()
			if err != nil {
				t.Fatal(err)
			}
			if audit.Corrected != 0 {
				t.Fatalf("%s: the sweep disagreed: %+v", tc.name, audit)
			}
		})
	}
}

// A libretto is machinery, not a conversation: no listing may draw it.
func TestLibrettoStumpsAreNeverListed(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("hidden", setPatch(map[string]string{"system.model": "m"}))
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
	if _, err := be.Libretto(sourceID); err != nil {
		t.Fatal(err)
	}
	for _, n := range be.Nodes() {
		if _, isLibretto := SourceOfLibretto(n.ID); isLibretto {
			t.Fatalf("a libretto stump appeared in a listing: %s", n.ID)
		}
	}
	for _, n := range be.Forms() {
		if _, isLibretto := SourceOfLibretto(n.ID); isLibretto {
			t.Fatalf("a libretto stump appeared among the forms: %s", n.ID)
		}
	}
}

// IMPORT restores a board wholesale, and `system.studies` is an ordinary key
// that an exported board carries -- so an import can name studied forms that
// nothing counted. durable-forms §12.2.2 lists it beside fork and kill, and
// it is the site I first wrote off as "this store has no import verb". It
// has one; the angelus calls this hook.
func TestRetainDeclaredStudiesCoversAnImportedBoard(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("imported", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	// A live observer, so the libretto exists and is counted once.
	live, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.StudyForm(live, sourceID); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}

	// The import: a new conversation whose restored board already names the
	// studied form, exactly as `angelus.import` applies it.
	imported, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]string{sourceID})
	if err != nil {
		t.Fatal(err)
	}
	// A HAND-WRITTEN study set is refused: `system.studies` is system-managed
	// because each entry is refcounted, and a board naming a study nothing
	// counted is the unrecoverable direction of §12.2.2, reachable from the
	// CLI until this key was protected.
	if _, _, err := be.ApplyFormEffect(imported, message.Patch{
		Set: map[string]json.RawMessage{StudiesKey: raw},
	}, 0); err == nil {
		t.Fatal("an unprivileged write of system.studies was allowed")
	}
	// The harness's own restore (a fork's board copy, an import replaying the
	// set) is privileged, and THAT is what the participant hook covers.
	if _, err := be.ApplyFormPrivileged(imported, message.Patch{
		Set: map[string]json.RawMessage{StudiesKey: raw},
	}); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs before the hook = %d, want 1: the import declared a study nothing counted", got)
	}
	be.RetainDeclaredStudies(imported)
	if got := lib.Refs(); got != 2 {
		t.Fatalf("refs after the hook = %d, want 2", got)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed after an import: %+v", audit)
	}

	// And the failure it prevents: the live observer drops, and the imported
	// aria's copy must survive.
	if _, _, err := be.DropForm(live, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("after the only OTHER observer dropped, refs = %d, want 1", got)
	}
	if lib.Reclaimable() {
		t.Fatal("a libretto an imported aria is still studying reported itself reclaimable")
	}
}

// SIXTEEN ARIAS STUDYING ONE FORM AT ONCE. Three races meet here and each
// one is silent if it is wrong: the libretto singleton (two openers, one
// instance, and the loser must not leave a second writer on the stump), the
// refcount's compare-and-set loop, and sixteen boards each doing a
// version-guarded read-modify-write of their own study set.
//
// The assertion that matters is not "no panic" but that the SWEEP agrees
// afterwards: the count derived from the boards is what reclamation trusts,
// so a lost retain is a copy reclaimed under a live observer.
func TestConcurrentStudiesOfOneForm(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("crowd", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	const observers = 16
	watchers := make([]string, observers)
	for i := range watchers {
		id, err := be.CreateConversation(outfit)
		if err != nil {
			t.Fatal(err)
		}
		watchers[i] = id
	}

	errs := make(chan error, observers)
	for _, w := range watchers {
		go func(w string) {
			_, _, err := be.StudyForm(w, sourceID)
			errs <- err
		}(w)
	}
	for range watchers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != observers {
		t.Fatalf("refs = %d after %d concurrent studies", got, observers)
	}
	open, obs := be.LibrettoStats()
	if open != 1 {
		t.Fatalf("%d libretto instances for one form", open)
	}
	if obs != observers {
		t.Fatalf("stats say %d observers, want %d", obs, observers)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed after concurrent studies: %+v", audit)
	}

	// And the same crowd dropping at once.
	for _, w := range watchers {
		go func(w string) {
			_, _, err := be.DropForm(w, sourceID)
			errs <- err
		}(w)
	}
	for range watchers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := lib.Refs(); got != 0 {
		t.Fatalf("refs = %d after everyone dropped", got)
	}
	audit, err = be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed after concurrent drops: %+v", audit)
	}
}

// A study that ultimately FAILS must not leave a reference behind. The
// retain happens before the first board write, and if the declaration never
// lands the count would otherwise stay high with nothing naming it -- a leak
// only the sweep could explain.
func TestAFailedStudyLeavesNoReference(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("failed", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	// Seal the observer's board: every write to it now fails, so the study
	// cannot be declared however many times it is retried.
	f, err := be.form(watcher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Tombstone("test: make the board unwritable"); err != nil {
		t.Fatal(err)
	}
	before := lib.Refs()
	if _, _, err := be.StudyForm(watcher, sourceID); err == nil {
		t.Fatal("studying onto a sealed board succeeded")
	}
	if got := lib.Refs(); got != before {
		t.Fatalf("a failed study left refs at %d, was %d", got, before)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep had to repair after a failed study: %+v", audit)
	}
}

// STUDY AND DROP RACING ON ONE FORM. The last drop closes the libretto's
// fold; a study arriving in that window retains an instance that is being
// torn down. Nothing here may end with a board naming a study whose copy has
// stopped following, which is the silent-staleness failure again.
func TestStudyAndDropRaceOnOneForm(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("race", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	const watchers = 6
	ids := make([]string, watchers)
	for i := range ids {
		id, err := be.CreateConversation(outfit)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}

	done := make(chan struct{})
	for _, w := range ids {
		go func(w string) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 12; i++ {
				if _, _, err := be.StudyForm(w, sourceID); err != nil {
					t.Errorf("study: %v", err)
					return
				}
				if _, _, err := be.DropForm(w, sourceID); err != nil {
					t.Errorf("drop: %v", err)
					return
				}
			}
		}(w)
	}
	for range ids {
		<-done
	}

	// Every board is clean, so the count must be zero and the sweep silent.
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 0 {
		t.Fatalf("refs = %d after every study was dropped", got)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed after study/drop racing: %+v", audit)
	}

	// And the survivor still follows: one more study, one more patch.
	if _, _, err := be.StudyForm(ids[0], sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "after the storm"})); err != nil {
		t.Fatal(err)
	}
	final, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the copy to follow after the race", func() bool {
		raw, ok := final.State().Get("brief")
		return ok && string(raw) == `"after the storm"`
	})
}
