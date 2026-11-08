package wire_test

import (
	"slices"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/topo"
)

func promotedPair(t *testing.T) (b *store.XwalBackend, stump, conv, alt string) {
	t.Helper()
	b, _ = backend(t, true)
	stump, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	if conv, err = b.CreateConversation(stump); err != nil {
		t.Fatal(err)
	}
	if _, alt, err = b.Fork(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store().Promote(alt, 1); err != nil {
		t.Fatal(err)
	}
	return b, stump, conv, alt
}

// The incident this guards against: an aria drawn at the top (the root,
// "null"), away from the form it was born of, must not fall back into that form's delete set when
// the clean runs, or the form's lifetime takes it.
func TestAnAriaDrawnAwayFromATTLdBirthFormSurvivesItsExpiryAfterAClean(t *testing.T) {
	b, _ := backend(t, true)
	f, _, err := b.CreateForm("", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := b.ForkWith(f, 0, patch("mantra", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Store().Tree().Reparent(x, "null"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyFormPrivileged(f, patch("system.ttl", "1ms")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	due := false
	for _, e := range b.TTLDue(time.Now().UnixMilli()) {
		due = due || e.ID == f
	}
	if !due {
		t.Fatal("setup: the birth form's lifetime is not spent")
	}
	if _, err := b.Store().CleanTopology(false); err != nil {
		t.Fatal(err)
	}
	taken := b.DeleteSet(f)
	if slices.Contains(taken, x) {
		t.Fatalf("the form's expiry would take %s, drawn at the top: %v", x, taken)
	}
	if err := b.RemoveIf(f, taken); err != nil {
		t.Fatal(err)
	}
	if _, err := b.FormState(x); err != nil {
		t.Fatalf("%s did not survive its birth form's expiry: %v", x, err)
	}
}

// The clean writes nothing when what it judged has moved on: an arrangement
// that lands mid-pass is kept whole.
func TestPruneRefusesWhenAnArrangementLandsMidPass(t *testing.T) {
	b, stump, conv, alt := promotedPair(t)
	tree := b.Store().Tree()
	_, err := tree.Prune(func(edges map[string]string) []topo.Stale {
		if err := tree.Reparent(conv, stump); err != nil {
			t.Fatal(err)
		}
		return []topo.Stale{{ID: alt, Reason: topo.StaleHistoryAgrees}}
	}, false)
	if err == nil {
		t.Fatal("prune wrote over an arrangement it did not see")
	}
	if _, ok := tree.Edges()[alt]; !ok {
		t.Fatal("prune dropped an override after refusing")
	}
	if _, ok := tree.Edges()[conv]; ok {
		t.Fatal("the arrangement that landed mid-pass was lost")
	}
}
