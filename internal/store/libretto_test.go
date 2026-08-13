package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
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
	if at, srcVersion := lib.At(), src.Version(); at > srcVersion {
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
