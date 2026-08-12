package wire_test

import (
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

// chain builds outfit -> a -> mid -> leaf.
func chain(t *testing.T, b *store.XwalBackend) (outfitID, a, mid, leaf string) {
	t.Helper()
	outfit, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	top, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := b.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	_, third, err := b.Fork(second)
	if err != nil {
		t.Fatal(err)
	}
	return outfit, top, second, third
}

func drawnUnder(t *testing.T, b *store.XwalBackend, id string) string {
	t.Helper()
	n, ok := b.Node(id)
	if !ok {
		t.Fatalf("no node for %s", id)
	}
	return n.Present
}

// A promote that no listing can see is a promote that did not happen.
func TestPromoteMovesTheDrawnRow(t *testing.T) {
	b, _ := backend(t, true)
	_, a, mid, leaf := chain(t, b)

	if _, err := b.Store().Promote(leaf, 1); err != nil {
		t.Fatal(err)
	}
	if got := drawnUnder(t, b, leaf); got != a {
		t.Errorf("leaf drawn under %q, want %q", got, a)
	}
	if got := drawnUnder(t, b, mid); got != leaf {
		t.Errorf("mid drawn under %q, want %q", got, leaf)
	}
	// The history edge is untouched: that is what forking reads.
	n, _ := b.Node(leaf)
	if n.Parent != mid {
		t.Errorf("history parent moved to %q", n.Parent)
	}
}

// The vector is the listing's tree coordinate, so it has to follow the row.
func TestPromoteMovesTheVector(t *testing.T) {
	b, _ := backend(t, true)
	_, _, mid, leaf := chain(t, b)

	before, _ := b.Node(leaf)
	if len(before.Vector) != 3 {
		t.Fatalf("leaf vector = %v, want depth 3", before.Vector)
	}
	if _, err := b.Store().Promote(leaf, 1); err != nil {
		t.Fatal(err)
	}
	after, _ := b.Node(leaf)
	if len(after.Vector) != 2 {
		t.Errorf("leaf vector = %v, want depth 2 after promote", after.Vector)
	}
	demoted, _ := b.Node(mid)
	if len(demoted.Vector) != 3 {
		t.Errorf("mid vector = %v, want depth 3 after being displaced", demoted.Vector)
	}
}

// An outfit stump and the genesis root are structure. Hanging one under a
// conversation puts every aria in the store inside that aria's subtree.
func TestPromoteStopsAtTheOutfit(t *testing.T) {
	b, _ := backend(t, true)
	_, a, _, _ := chain(t, b)

	if _, err := b.Store().Promote(a, 1); !errors.Is(err, store.ErrAtStump) {
		t.Fatalf("promote of a top-level aria = %v, want ErrAtStump", err)
	}
	if got := drawnUnder(t, b, a); got == "" {
		t.Fatal("the top-level aria lost its outfit")
	}
}

// Asking for more levels than the tree has climbs what it can and says so.
func TestPromoteClimbsWhatItCan(t *testing.T) {
	b, _ := backend(t, true)
	outfit, _, _, leaf := chain(t, b)

	climbed, err := b.Store().Promote(leaf, 10)
	if err != nil {
		t.Fatal(err)
	}
	if climbed != 2 {
		t.Errorf("climbed %d, want 2 (the outfit is the ceiling)", climbed)
	}
	if got := drawnUnder(t, b, leaf); got != outfit {
		t.Errorf("leaf drawn under %q, want the outfit %q", got, outfit)
	}
}

// A refused delete must leave the store exactly as it found it: the repair
// it would have performed cannot be undone.
func TestRefusedDeleteWritesNothing(t *testing.T) {
	b, _ := backend(t, true)
	_, _, mid, leaf := chain(t, b)

	before, _ := b.Node(leaf)
	if err := b.Remove(mid, false); !errors.Is(err, store.ErrHasBranches) {
		t.Fatalf("delete of a branched aria = %v, want ErrHasBranches", err)
	}
	after, ok := b.Node(leaf)
	if !ok {
		t.Fatal("the branch vanished on a refused delete")
	}
	if after.Parent != before.Parent {
		t.Errorf("the branch moved on a refused delete: %q -> %q", before.Parent, after.Parent)
	}
}

// A recursive delete takes the whole drawn subtree with it, and leaves no
// edge behind naming an aria that is gone.
func TestRecursiveDeleteTakesTheSubtree(t *testing.T) {
	b, _ := backend(t, true)
	_, _, mid, leaf := chain(t, b)

	if err := b.Remove(mid, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{mid, leaf} {
		if _, ok := b.Node(id); ok {
			t.Errorf("%s survived a recursive delete", id)
		}
	}
	if edges := b.Store().Tree().Edges(); len(edges) != 0 {
		t.Errorf("edges survived the delete: %v", edges)
	}
}

// Absorbing a survivor's prefix is a storage repair. Without a re-home it
// is also a teleport to the genesis root, which is how a store ends up
// flat.
func TestDeleteKeepsASurvivorWhereItWasDrawn(t *testing.T) {
	b, _ := backend(t, true)
	_, a, mid, leaf := chain(t, b)

	if _, err := b.Store().Promote(leaf, 1); err != nil { // leaf now sits under a
		t.Fatal(err)
	}
	if err := b.Remove(mid, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Node(leaf); !ok {
		t.Fatal("a promoted survivor was taken by its old parent's delete")
	}
	if got := drawnUnder(t, b, leaf); got != a {
		t.Errorf("survivor drawn under %q, want %q", got, a)
	}
}
