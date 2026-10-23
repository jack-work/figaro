package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

// A lifetime stated at birth is a lifetime: the birth patch is appended to
// the form channel directly by writeBirth, bypassing Form.OnCommit, so the
// sidecar mirror must be taken at birth too.
func TestTTLStatedAtBirthReachesTheSidecar(t *testing.T) {
	b := newTTLBackend(t)
	outfit, err := b.CreateOutfit("ttl", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	born, _, err := b.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		SystemTTLKey: json.RawMessage(`"10m"`),
		"mantra":     json.RawMessage(`"one-off"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ScanTTL(b.root)[born]
	if !ok {
		t.Fatalf("an aria born with %s is not in the sidecar index: the reaper will never remove it", SystemTTLKey)
	}
	if e.TTL != 10*time.Minute {
		t.Errorf("ttl %v, want 10m", e.TTL)
	}
	if !containsTTL(b.TTLEntries(), born) {
		t.Error("the in-memory deadline set does not hold the birth's lifetime")
	}

	plain, _, err := b.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"keeps"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ScanTTL(b.root)[plain]; ok {
		t.Error("a birth that states no lifetime was given one")
	}

	unbound, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		SystemTTLKey: json.RawMessage(`"2h"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := ScanTTL(b.root)[unbound]; !ok || e.TTL != 2*time.Hour {
		t.Errorf("an unbound form born with a 2h lifetime: indexed=%v ttl=%v", ok, e.TTL)
	}
}

func containsTTL(entries []TTLEntry, id string) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

func outfitWithTTL(t *testing.T, b *XwalBackend, ttl string) string {
	t.Helper()
	raw, _ := json.Marshal(ttl)
	outfit, err := b.CreateOutfit("short", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		SystemTTLKey: raw,
		"skills.x":   json.RawMessage(`1`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return outfit
}

// Issue #48: a lifetime that reaches the bound form through the outfit, never
// through a patch that names it, was never indexed and so never reaped.
func TestTTLLayeredByTheOutfitIsIndexedAtBirth(t *testing.T) {
	b := newTTLBackend(t)
	outfit := outfitWithTTL(t, b, "1h")

	created, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	forked, _, err := b.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"no lifetime named"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"create": created, "fork of the outfit": forked} {
		e, ok := ScanTTL(b.root)[id]
		if !ok || e.TTL != time.Hour {
			t.Errorf("%s: indexed=%v ttl=%v, want the outfit's 1h", name, ok, e.TTL)
		}
		if !containsTTL(b.TTLEntries(), id) {
			t.Errorf("%s: the in-memory deadline set misses the outfit's lifetime", name)
		}
	}
}

func TestTTLInheritedByAForkIsIndexed(t *testing.T) {
	b := newTTLBackend(t)
	parent := mustNewAria(t, b)
	setBoundFormKey(t, b, parent, SystemTTLKey, "3h")

	child, _, err := b.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"child"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := ScanTTL(b.root)[child]; !ok || e.TTL != 3*time.Hour {
		t.Errorf("fork of a 3h aria: indexed=%v ttl=%v", ok, e.TTL)
	}

	dropped, _, err := b.ForkWith(parent, 0, form.Build(form.Snapshot{}, nil, []string{SystemTTLKey}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ScanTTL(b.root)[dropped]; ok {
		t.Error("a fork whose patch drops the lifetime was indexed with its parent's")
	}
}

// A bound form that holds a lifetime the sidecar lost (an aria born before the
// birth mirror) is reconciled when an outfit is dressed onto it, even if the
// dress repeats the lifetime it already holds.
func TestTTLReconciledWhenDressed(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoundFormKey(t, b, id, SystemTTLKey, "2h")
	meta, _ := b.Meta(id)
	meta.TTLMS = 0
	if err := b.SetMeta(id, meta); err != nil {
		t.Fatal(err)
	}
	if _, ok := ScanTTL(b.root)[id]; ok {
		t.Fatal("setup: the sidecar still holds the lifetime")
	}

	if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		keyOutfitName: json.RawMessage(`"short"`),
		SystemTTLKey:  json.RawMessage(`"2h"`),
	}, nil)); err != nil {
		t.Fatal(err)
	}
	if e, ok := ScanTTL(b.root)[id]; !ok || e.TTL != 2*time.Hour {
		t.Errorf("after a dress: indexed=%v ttl=%v, want 2h", ok, e.TTL)
	}
}
