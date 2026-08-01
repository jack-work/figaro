package topo

import (
	"errors"
	"sort"
	"testing"
)

// fake is a topology: id -> its .from parent.
type fake map[string]string

func (f fake) From(id string) (string, bool) { p, ok := f[id]; return p, ok }
func (f fake) Nodes() []string {
	out := make([]string, 0, len(f))
	for k := range f {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func (f fake) ChildrenOf(id string) []string {
	var out []string
	for k, p := range f {
		if p == id {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// The diagram: B and C fork from A, D forks from C.
func forest() fake { return fake{"A": "", "B": "A", "C": "A", "D": "C"} }

func TestTrunklessDeleteTakesTheSubtree(t *testing.T) {
	tree := FromTopology(forest())
	got := tree.DeleteSet("C")
	sort.Strings(got)
	if len(got) != 2 || got[0] != "C" || got[1] != "D" {
		t.Fatalf("DeleteSet(C) = %v, want [C D]", got)
	}
}

// Without the capability the two trees are the same, so a delete can never
// orphan anyone. This is the property that lets a trunkless figaro skip
// boundary repair entirely.
func TestTrunklessBoundaryIsAlwaysEmpty(t *testing.T) {
	f := forest()
	tree := FromTopology(f)
	for _, id := range f.Nodes() {
		if b := Boundary(f, tree.DeleteSet(id)); len(b) != 0 {
			t.Errorf("Boundary(delete %s) = %v, want empty", id, b)
		}
	}
}

// The hole promotion opens: delete A's trunk-subtree and B, which still
// inherits A's prefix, is left pointing at a directory that is gone.
func TestBoundaryCatchesTheOrphan(t *testing.T) {
	f := forest()
	b := Boundary(f, []string{"A", "C", "D"}) // the trunk closure after B is promoted
	if len(b) != 1 || b[0] != "B" {
		t.Fatalf("Boundary = %v, want [B]: B inherits A's prefix", b)
	}
}

func TestTrunklessPromoteIsNotSilent(t *testing.T) {
	if err := FromTopology(forest()).Promote("B"); !errors.Is(err, ErrNoPromote) {
		t.Fatalf("Promote error = %v, want ErrNoPromote", err)
	}
}
