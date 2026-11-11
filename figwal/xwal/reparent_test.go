package xwal

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// A successor's turn-1 fork lands under the first holder; moving it under the
// successor copies nothing and changes nothing it reads.
func TestReparentMovesWithoutCopying(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	appendAll := func(id string, ps ...string) {
		for _, p := range ps {
			if _, _, err := f.Append(id, 0, []byte(`"`+p+`"`), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendAll(trunk, "a", "b")
	h0, err := f.ForkAt(trunk, 1)
	if err != nil {
		t.Fatal(err)
	}
	appendAll(h0, "h0a", "h0b")
	h1, err := f.ForkAt(h0, 2)
	if err != nil {
		t.Fatal(err)
	}
	appendAll(h1, "h1")
	lieutenant, err := f.ForkAt(h1, 1)
	if err != nil {
		t.Fatal(err)
	}
	key := func(id string) string { k, _ := f.idx.Head(id); return k }
	if got := f.idx.ParentOf(key(lieutenant)); got == key(h1) {
		t.Fatal("setup: the turn-1 fork already sits under the successor")
	}
	before := mainPayloads(t, f, lieutenant)

	walks := f.idx.rebuilds.Load()
	if err := f.Reparent(key(lieutenant), key(h1)); err != nil {
		t.Fatalf("reparent: %v", err)
	}
	if got := f.idx.ParentOf(key(lieutenant)); got != key(h1) {
		t.Fatalf("parent = %q, want %q", got, key(h1))
	}
	if f.idx.rebuilds.Load() != walks {
		t.Fatal("a move walked every marker in the store")
	}
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.idx.ParentOf(key(lieutenant)); got != key(h1) {
		t.Fatalf("after a rescan the parent is %q, want %q: the marker was not written", got, key(h1))
	}
	if after := mainPayloads(t, f, lieutenant); !slices.Equal(before, after) {
		t.Fatalf("history changed: %v -> %v", before, after)
	}

	deep, err := f.ForkAt(h0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reparent(key(deep), key(h1)); !errors.Is(err, ErrMoveCopies) {
		t.Fatalf("a move that would copy: err = %v, want ErrMoveCopies", err)
	}
	if err := f.Reparent(key(h0), key(h1)); err == nil {
		t.Fatal("moved a node under its own descendant")
	}
}
