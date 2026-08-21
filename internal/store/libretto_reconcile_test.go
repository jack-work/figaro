package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// Reconciliation RECOMPUTES, which is the whole reason it exists: the study
// path's ordering only guarantees an over-count, and fork/import/kill break
// it the other way (durable-forms §12.2.2). A sweep that adjusted rather
// than recomputed would repair one direction and not the other.

func studySet(t *testing.T, be *XwalBackend, ariaID string, forms ...string) {
	t.Helper()
	raw, err := json.Marshal(forms)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyFormPrivileged(ariaID, message.Patch{Set: map[string]json.RawMessage{StudiesKey: raw}}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileRepairsBothDirections(t *testing.T) {
	be, sourceID, src := librettoFixture(t)
	_ = src
	// Two figaros studying the same form, and one studying nothing.
	outfit, err := be.CreateOutfit("recon", setPatch(map[string]string{"system.model": "m"}))
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
	if _, err := be.CreateConversation(outfit); err != nil {
		t.Fatal(err)
	}
	studySet(t, be, watcherA, sourceID)
	studySet(t, be, watcherB, sourceID)

	// THE SHARED instance, not a fresh one: a second Libretto over the same
	// stump is a second writer on its channel, which is what this sweep was
	// caught doing (see TestSweepDoesNotCreateASecondWriter).
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}

	// UNDER-count, which is the unrecoverable direction: a fork inherited the
	// study set and nothing incremented.
	if err := lib.setRefs(0); err != nil {
		t.Fatal(err)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 1 {
		t.Fatalf("corrected %d librettos, want 1 (audit %+v)", audit.Corrected, audit)
	}
	again, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Refs(); got != 2 {
		t.Fatalf("refs after repairing an under-count = %d, want 2", got)
	}

	// OVER-count: a crash between the libretto write and the board write.
	if err := again.setRefs(7); err != nil {
		t.Fatal(err)
	}
	if _, err := be.ReconcileLibrettos(); err != nil {
		t.Fatal(err)
	}
	third, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := third.Refs(); got != 2 {
		t.Fatalf("refs after repairing an over-count = %d, want 2", got)
	}

	// Idempotent: a second pass corrects nothing.
	audit, err = be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("a second pass corrected %d (audit %+v)", audit.Corrected, audit)
	}
	if audit.Librettos != 1 {
		t.Fatalf("examined %d librettos, want 1", audit.Librettos)
	}
}

// A libretto nobody names is reported as orphaned and counted to zero: that
// is the state reclamation acts on, and it must be reached by recomputation
// rather than by trusting a decrement that may never have happened.
func TestReconcileZeroesAnOrphan(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Retain(); err != nil {
		t.Fatal(err)
	}

	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Orphaned != 1 {
		t.Fatalf("orphaned = %d, want 1 (audit %+v)", audit.Orphaned, audit)
	}
	again, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reclaimable() {
		t.Fatalf("an orphan still reports %d refs", again.Refs())
	}
}

// A studied form with no libretto is REPORTED by an audit and MINTED by a
// repair. That is the migration: a store from before phase 9 carries studies
// whose librettos were never created, and the verb cannot make them because
// it only runs at study time.
func TestReconcileReportsAStudiedFormWithNoLibretto(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("recon2", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"brief": "pre-existing"})); err != nil {
		t.Fatal(err)
	}
	studySet(t, be, watcher, sourceID)
	// An AUDIT reports and changes nothing.
	audit, err := be.AuditLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Missing != 1 {
		t.Fatalf("missing = %d, want 1 (audit %+v)", audit.Missing, audit)
	}
	if audit.Librettos != 0 {
		t.Fatalf("examined %d librettos where none exist", audit.Librettos)
	}
	if be.HasLibrettos() {
		t.Fatal("the audit created a libretto")
	}

	// A REPAIR mints it, seeds it from the source, counts it -- and REPORTS
	// it as minted rather than as still missing. A repair tool that reports
	// the pre-state describes work it has just finished as outstanding.
	repaired, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Minted != 1 {
		t.Fatalf("minted = %d, want 1 (%+v)", repaired.Minted, repaired)
	}
	if repaired.Missing != 0 {
		t.Fatalf("missing = %d after minting it, want 0 (%+v)", repaired.Missing, repaired)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("the migrated libretto has %d refs, want 1", got)
	}
	waitFor(t, "the migrated copy to carry the source's state", func() bool {
		_, ok := lib.State().Get("brief")
		return ok
	})

	// And it is idempotent: nothing is missing or corrected on a second pass.
	audit, err = be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Missing != 0 || audit.Corrected != 0 {
		t.Fatalf("a second pass still has work to do: %+v", audit)
	}
}

// The boot sweep must cost NOTHING on a store that has never studied
// anything, which is every store until the verb is used. The guard is a
// stump-name scan that touches no board.
func TestHasLibrettosIsCheapAndHonest(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	if be.HasLibrettos() {
		t.Fatal("a store with no studies reported librettos")
	}
	lib, err := OpenLibretto(be.Store(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if !be.HasLibrettos() {
		t.Fatal("a store with a libretto reported none: the boot sweep would never run")
	}
}
