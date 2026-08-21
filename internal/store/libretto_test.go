package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
)

// The libretto is a COPY that follows one source. These are the properties
// the rest of phase 9 will rest on, so they are stated here rather than
// inferred from the verb that will drive them.

func librettoFixture(t *testing.T) (*XwalBackend, string, *Form) {
	t.Helper()
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("lib", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	src, err := be.form(id)
	if err != nil {
		t.Fatal(err)
	}
	return be, id, src
}

func setPatch(kv map[string]string) message.Patch {
	set := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		raw, _ := json.Marshal(v)
		set[k] = raw
	}
	return message.Patch{Set: set}
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLibrettoIDIsDerivedFromTheSource(t *testing.T) {
	if got := LibrettoID("@3c00e173"); got != "@libretto::3c00e173" {
		t.Fatalf("LibrettoID = %q", got)
	}
	if got := LibrettoID("3c00e173"); got != "@libretto::3c00e173" {
		t.Fatalf("LibrettoID without the sigil = %q", got)
	}
	src, ok := SourceOfLibretto("@libretto::3c00e173")
	if !ok || src != "3c00e173" {
		t.Fatalf("SourceOfLibretto = %q, %v", src, ok)
	}
	if _, ok := SourceOfLibretto("@topology"); ok {
		t.Fatal("a reserved stump that is not a libretto was read as one")
	}
}

// The first fold must be the source's WHOLE state: a libretto that started
// mid-history would render a form that never existed.
func TestLibrettoSeedsWithTheWholeSourceState(t *testing.T) {
	be, id, src := librettoFixture(t)
	if _, err := be.ApplyForm(id, setPatch(map[string]string{
		"brief": "ship the thing", "status": "open",
	})); err != nil {
		t.Fatal(err)
	}
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if err := lib.Follow(src); err != nil {
		t.Fatal(err)
	}
	st := lib.State()
	for k, want := range map[string]string{"brief": "ship the thing", "status": "open"} {
		raw, ok := st.Get(k)
		if !ok {
			t.Fatalf("libretto is missing %q from the seed", k)
		}
		var got string
		json.Unmarshal(raw, &got)
		if got != want {
			t.Fatalf("libretto %q = %q, want %q", k, got, want)
		}
	}
	if !lib.Alive() {
		t.Fatal("a fresh libretto is not alive")
	}
	if lib.Refs() != 0 {
		t.Fatalf("a fresh libretto has %d refs, want 0", lib.Refs())
	}
}

func TestLibrettoFollowsThePatchesAfterIt(t *testing.T) {
	be, id, src := librettoFixture(t)
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if err := lib.Follow(src); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := be.ApplyForm(id, setPatch(map[string]string{
			"brief": fmt.Sprintf("v%d", i),
		})); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the libretto to catch up", func() bool {
		raw, ok := lib.State().Get("brief")
		if !ok {
			return false
		}
		var got string
		json.Unmarshal(raw, &got)
		return got == "v19"
	})
	// The cursor is the SOURCE's version, and it must not run ahead of it.
	if at, srcVersion := lib.At(), src.Read().Version; at > srcVersion {
		t.Fatalf("libretto cursor %d is ahead of the source at %d", at, srcVersion)
	}
	// A removal is a fold too, not just a set.
	if _, err := be.ApplyForm(id, message.Patch{Remove: []string{"brief"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removal to reach the libretto", func() bool {
		_, ok := lib.State().Get("brief")
		return !ok
	})
}

// The source's death is a RECORD the libretto keeps: the copy survives, so a
// studied form can be deleted at all, and history still renders afterwards.
func TestLibrettoRecordsTheSourcesDeathAndKeepsTheCopy(t *testing.T) {
	be, id, src := librettoFixture(t)
	if _, err := be.ApplyForm(id, setPatch(map[string]string{"brief": "alive"})); err != nil {
		t.Fatal(err)
	}
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if err := lib.Follow(src); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Tombstone("test"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the death to be recorded", func() bool { return !lib.Alive() })
	raw, ok := lib.State().Get("brief")
	if !ok {
		t.Fatal("the copy was lost with the source")
	}
	var got string
	json.Unmarshal(raw, &got)
	if got != "alive" {
		t.Fatalf("the copy says %q", got)
	}
}

// Refcounting decides reclamation, so a lost increment is data loss and a
// lost decrement is a leak. The count lives on the libretto's own actor,
// which is what makes "reached zero" a fact rather than a sample.
func TestLibrettoRefcountIsSerializedAndRefusesToGoNegative(t *testing.T) {
	be, id, _ := librettoFixture(t)
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	const observers = 32
	done := make(chan error, observers)
	for i := 0; i < observers; i++ {
		go func() {
			_, err := lib.Retain()
			done <- err
		}()
	}
	for i := 0; i < observers; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := lib.Refs(); got != observers {
		t.Fatalf("refs = %d after %d concurrent retains", got, observers)
	}
	if lib.Reclaimable() {
		t.Fatal("a libretto with observers reported itself reclaimable")
	}
	for i := 0; i < observers; i++ {
		go func() {
			_, err := lib.Release()
			done <- err
		}()
	}
	for i := 0; i < observers; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := lib.Refs(); got != 0 {
		t.Fatalf("refs = %d after releasing every retain", got)
	}
	if !lib.Reclaimable() {
		t.Fatal("a libretto nobody studies is not reclaimable")
	}
	if _, err := lib.Release(); err == nil {
		t.Fatal("releasing below zero was allowed: reclamation would never collect it")
	}
}

// A libretto is durable state, not a cache: what it folded survives the
// process that folded it.
func TestLibrettoSurvivesAReopen(t *testing.T) {
	be, id, src := librettoFixture(t)
	if _, err := be.ApplyForm(id, setPatch(map[string]string{"brief": "durable"})); err != nil {
		t.Fatal(err)
	}
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.Follow(src); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Retain(); err != nil {
		t.Fatal(err)
	}
	at := lib.At()
	lib.Close()

	again, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.Refs() != 1 {
		t.Fatalf("refs after reopen = %d, want 1", again.Refs())
	}
	if again.At() != at {
		t.Fatalf("cursor after reopen = %d, want %d", again.At(), at)
	}
	raw, ok := again.State().Get("brief")
	if !ok {
		t.Fatal("the copy did not survive the reopen")
	}
	var got string
	json.Unmarshal(raw, &got)
	if got != "durable" {
		t.Fatalf("the reopened copy says %q", got)
	}
}

// The mirror copies the source's keys verbatim, so a source that happens to
// carry the libretto's own bookkeeping namespace must not overwrite it.
func TestLibrettoNeverMirrorsItsOwnBookkeeping(t *testing.T) {
	be, id, src := librettoFixture(t)
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if err := lib.Follow(src); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Retain(); err != nil {
		t.Fatal(err)
	}
	// Privileged, because system.* is exactly what an ordinary write cannot
	// touch -- which is the reason the namespace was chosen.
	if _, _, err := src.ApplyEffectPrivileged(librettoPatch(map[string]any{
		KeyLibrettoRefs: 99, "brief": "sneaky",
	}), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ordinary key to arrive", func() bool {
		raw, ok := lib.State().Get("brief")
		if !ok {
			return false
		}
		var got string
		json.Unmarshal(raw, &got)
		return got == "sneaky"
	})
	if got := lib.Refs(); got != 1 {
		t.Fatalf("the source overwrote the libretto's refcount: refs = %d", got)
	}
}

// The fold coalesces, so a run of source patches becomes ONE durable write on
// the copy. What must NOT change is the STATE it arrives at: later events win
// per key, in both directions, or a mirror ends up holding a value the source
// does not.
func TestLibrettoBatchFoldEndsWhereTheSourceDoes(t *testing.T) {
	be, id, src := librettoFixture(t)
	lib, err := OpenLibretto(be.Store(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	if err := lib.Follow(src); err != nil {
		t.Fatal(err)
	}
	// A burst: set, remove, re-set, and a key that ends removed. Written as
	// fast as the source will take them, so the fold sees them batched.
	for i := 0; i < 20; i++ {
		if _, err := be.ApplyForm(id, setPatch(map[string]string{
			"kept":   fmt.Sprintf("v%d", i),
			"doomed": "here",
		})); err != nil {
			t.Fatal(err)
		}
		if _, err := be.ApplyForm(id, message.Patch{Remove: []string{"doomed"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := be.ApplyForm(id, setPatch(map[string]string{"last": "word"})); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the copy to reach the source's last word", func() bool {
		raw, ok := lib.State().Get("last")
		if !ok {
			return false
		}
		var got string
		json.Unmarshal(raw, &got)
		return got == "word"
	})
	// Every key the source ends with, the copy ends with -- and nothing else.
	srcSnap, _ := src.Snapshot()
	for k, want := range srcSnap.All() {
		if isLibrettoKey(k) {
			continue
		}
		got, ok := lib.State().Get(k)
		if !ok {
			t.Fatalf("the copy is missing %q after a coalesced fold", k)
		}
		if string(got) != string(want) {
			t.Fatalf("copy %q = %s, source has %s", k, got, want)
		}
	}
	if _, ok := lib.State().Get("doomed"); ok {
		t.Fatal("a key the source removed survived the coalesced fold")
	}
}

// THE ORPHANED READER. The libretto's Form is the writer the fold appends
// through. A reader that opens its own Form over the same stump replays once
// and never hears that writer again, so it renders one correct study block
// and then freezes -- which is exactly what the translator did the first time
// it read a libretto, while every unit test and every refcount stayed green.
//
// Two halves: the shared instance sees a fold that happens AFTER it was
// handed out, and the node path refuses to hand out a second one at all.
func TestLibrettoReaderSeesFoldsAfterItWasOpened(t *testing.T) {
	be, src, _ := librettoFixture(t)
	aria, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(aria, src); err != nil {
		t.Fatal(err)
	}

	// The accessor is taken ONCE, as a turn takes it, and held.
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	at := lib.Version()

	v, err := be.ApplyForm(src, patchOf(t, map[string]string{"afterwards": `"yes"`}))
	if err != nil {
		t.Fatal(err)
	}
	waitForFold(t, lib, v)

	found := false
	for _, p := range lib.PatchesBetween(at, lib.Version()) {
		if _, ok := p.Patch.Set["afterwards"]; ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("the held accessor froze at %d: a source change after it was opened never rendered", at)
	}

	// And the wrong door is locked: reading a libretto as a node would open
	// that second Form.
	if _, err := be.FormVersion(LibrettoID(src)); err == nil {
		t.Fatal("reading a libretto as a node succeeded; that is the orphaned reader")
	}
}

// THE DEATH ENDS THE LISTENING (wym.md:21, the half that was never built).
//
// A subscription that outlives its source pins that Form resident forever:
// the idle sweep refuses to evict anything subscribed, so the corpse of every
// studied-then-deleted form would be held for the daemon's life. The copy
// must stay READABLE while ceasing to listen -- that is the whole point of it
// being a copy.
func TestLibrettoStopsListeningWhenItsSourceDies(t *testing.T) {
	be, src, srcForm := librettoFixture(t)
	aria, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(aria, src); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	if !lib.Following() {
		t.Fatal("not following its source before the death")
	}
	before := lib.Version()

	if _, err := srcForm.Tombstone("test"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the listening to stop", func() bool { return !lib.Following() })
	if lib.Alive() {
		t.Error("the death was not recorded, but the listening stopped anyway")
	}

	// The copy outlives the source, which is what makes a studied form
	// deletable at all: its history is still readable AND the death itself
	// arrives as an ordinary key transition the render can show.
	sawDeath := false
	for _, p := range lib.PatchesBetween(before, lib.Version()) {
		if _, ok := p.Patch.Set[KeyLibrettoAlive]; ok {
			sawDeath = true
		}
	}
	if !sawDeath {
		t.Error("the death is not in the copy's history, so no render can name it")
	}
}

// The refusal must EXPLAIN ITSELF. A release with no matching retain is an
// under-count, the direction the sweep cannot repair, and it has so far
// appeared once in a loaded run and in none of the four that chased it.
// Repetition is not a hunting method for that; a message that names the moves
// which led to it is.
func TestReleaseBelowZeroCarriesTheLedger(t *testing.T) {
	be, src, _ := librettoFixture(t)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Retain(); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Release(); err != nil {
		t.Fatal(err)
	}
	_, err = lib.Release() // one too many, deliberately
	if err == nil {
		t.Fatal("releasing below zero was allowed")
	}
	msg := err.Error()
	for _, want := range []string{"release below zero", "recent refcount moves", "retain", "release"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q:\n%s", want, msg)
		}
	}
	// And it names WHERE, so the caller with no matching retain is visible
	// rather than inferred.
	if !strings.Contains(msg, "libretto_test.go") {
		t.Errorf("the ledger names no call site:\n%s", msg)
	}
}

// The refcount survives concurrent movers. Each goroutine retains and
// releases in balanced pairs, so the count must end exactly where it began --
// and no release may ever be refused, because none of them is unmatched.
//
// HONESTY ABOUT WHAT THIS PROVES: it does NOT prove the two-atomic-loads fix.
// Run against the old two-load version -- 8 runs, 1600 balanced pairs, under
// -race -- it stayed green: the window between two atomic loads is narrower
// than this harness can hit. It guards the property from here on; the
// argument for the fix is that the window EXISTS by construction, not that
// this test found it. The ledger on a refusal is what will read out the real
// occurrence if the window is not the cause.
func TestRefcountSurvivesConcurrentMovers(t *testing.T) {
	be, src, _ := librettoFixture(t)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	// A floor, so a lost update cannot be masked by the below-zero refusal.
	for i := 0; i < 8; i++ {
		if _, err := lib.Retain(); err != nil {
			t.Fatal(err)
		}
	}
	before := lib.Refs()

	const movers, rounds = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, movers)
	for i := 0; i < movers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				if _, err := lib.Retain(); err != nil {
					errs <- err
					return
				}
				if _, err := lib.Release(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a balanced mover was refused: %v", err)
	}
	if got := lib.Refs(); got != before {
		t.Fatalf("refs = %d, want %d: %d balanced pairs lost %d references",
			got, before, movers*rounds, before-got)
	}
}

// READERS WHILE IT FOLDS. Every observer of a form now renders from ONE
// shared Libretto instance (a second Form over that channel is an orphaned
// reader, refused by construction elsewhere), so the copy is read by N
// goroutines while its own fold writes it.
//
// That case had never been exercised: the fleet studies AFTER its patches, so
// twelve arias never raced the writer. This is the gap, closed.
//
// What it asserts is what a renderer depends on: a range asked for below the
// version the reader observed is answered without tearing, the answers are
// monotone in version, and nothing panics under -race.
func TestLibrettoReadersWhileItFolds(t *testing.T) {
	be, src, _ := librettoFixture(t)
	aria, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(aria, src); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := be.ApplyForm(src, patchOf(t, map[string]string{
				"n": fmt.Sprintf("%d", i)})); err != nil {
				return
			}
		}
	}()

	const readers = 8
	var rg sync.WaitGroup
	bad := make(chan string, readers)
	for r := 0; r < readers; r++ {
		rg.Add(1)
		go func() {
			defer rg.Done()
			var last uint64
			for i := 0; i < 200; i++ {
				at := lib.Version()
				if at < last {
					bad <- fmt.Sprintf("version went backwards: %d then %d", last, at)
					return
				}
				last = at
				// The renderer's exact call: an absolute window ending at a
				// version this reader has observed.
				for _, p := range lib.PatchesBetween(0, at) {
					if p.Version > at {
						bad <- fmt.Sprintf("a patch at %d came back for a window ending %d", p.Version, at)
						return
					}
				}
			}
		}()
	}
	rg.Wait()
	close(stop)
	writer.Wait()
	close(bad)
	for msg := range bad {
		t.Fatal(msg)
	}

	// And the copy is still coherent afterwards: one writer, one instance.
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep had to repair after concurrent reads: %+v", audit)
	}
}

// Alive is the tests' read of the death record on the copy. The product
// renders the key itself (system.libretto.alive); no shipped code needs
// the accessor, so it lives with its callers.
func (l *Libretto) Alive() bool {
	raw, ok := l.formState().Get(KeyLibrettoAlive)
	if !ok {
		return true
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return true
	}
	return b
}
