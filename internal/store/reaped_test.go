package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func TestReapedMirrorsIndexesAndClears(t *testing.T) {
	b := newTTLBackend(t)
	root := mustNewAria(t, b)
	child, _, err := b.ForkWith(root, 0, setPatch(map[string]string{"mantra": "child"}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(b.Reaped()); n != 0 {
		t.Fatalf("%d reaped before anything was reaped", n)
	}
	changes := 0
	b.OnReapedChange(func() { changes++ })

	setRuntimeKey(t, b, root, ReapedKey, root)
	setRuntimeKey(t, b, child, ReapedKey, root)
	got := b.Reaped()
	if len(got) != 2 || got[0].Root != root || got[1].Root != root {
		t.Fatalf("reaped %+v, want both nodes under root %s", got, root)
	}
	if changes != 2 {
		t.Errorf("%d change notices, want 2: the index form republishes on each", changes)
	}

	b.ttlMu.Lock()
	b.ttl, b.reaped = nil, nil
	b.ttlMu.Unlock()
	if n := len(b.Reaped()); n != 2 {
		t.Fatalf("a fresh scan found %d reaped; the sidecar is the record and must survive a restart", n)
	}

	removeRuntimeKey(t, b, child, ReapedKey)
	if got = b.Reaped(); len(got) != 1 || got[0].ID != root {
		t.Fatalf("reaped %+v after restoring the child, want only the root", got)
	}
}

func TestAForkOfAReapedAriaIsNotReaped(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setRuntimeKey(t, b, id, ReapedKey, id)
	child, _, err := b.ForkWith(id, 0, setPatch(map[string]string{"mantra": "revived"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range b.Reaped() {
		if e.ID == child {
			t.Fatal("a fork of a reaped aria is reaped; a fork is how a reaped conversation continues")
		}
	}
}

func TestAHardDeleteForgetsTheReapedEntry(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setRuntimeKey(t, b, id, ReapedKey, id)
	if err := b.Remove(id, true); err != nil {
		t.Fatal(err)
	}
	if n := len(b.Reaped()); n != 0 {
		t.Fatalf("%d reaped after the hard delete", n)
	}
}

func TestReapedByMirrorsIntoTheIndexAndGoesWithTheReap(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setRuntimeKey(t, b, id, ReapedKey, id)
	setRuntimeKey(t, b, id, ReapedByKey, "gc:run-1")
	if got := b.Reaped(); len(got) != 1 || got[0].By != "gc:run-1" {
		t.Fatalf("reaped %+v, want one entry by gc:run-1", got)
	}
	b.ttlMu.Lock()
	b.ttl, b.reaped = nil, nil
	b.ttlMu.Unlock()
	if got := b.Reaped(); len(got) != 1 || got[0].By != "gc:run-1" {
		t.Fatalf("after a fresh scan %+v: the sidecar must carry who reaped it", got)
	}
	child, _, err := b.ForkWith(id, 0, setPatch(map[string]string{"mantra": "revived"}))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := b.Meta(child); m != nil && m.ReapedBy != "" {
		t.Fatalf("a fork inherited reaped_by %q", m.ReapedBy)
	}
	if snap, err := b.FormState(child); err != nil || snap.Lookup(ReapedByKey) != nil {
		t.Fatalf("a fork's form carries %s: %v", ReapedByKey, err)
	}
	removeRuntimeKey(t, b, id, ReapedKey)
	if m, _ := b.Meta(id); m == nil || m.ReapedBy != "" {
		t.Fatalf("a restored aria still says who reaped it: %+v", m)
	}
}

func setRuntimeKey(t *testing.T, b *XwalBackend, id, key, val string) {
	t.Helper()
	raw, _ := json.Marshal(val)
	if _, err := b.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil)); err != nil {
		t.Fatal(err)
	}
}

func removeRuntimeKey(t *testing.T, b *XwalBackend, id, key string) {
	t.Helper()
	if _, err := b.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, nil, []string{key})); err != nil {
		t.Fatal(err)
	}
}
