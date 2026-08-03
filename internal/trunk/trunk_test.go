package trunk

import (
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/topo"
)

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

// The diagram: B and C fork from A, D forks from C -- under the null root,
// which is the only node with no .from and the one ancestor no delete can
// take. Leaving it out would make A look like a root and quietly change
// what "has an ancestor to lose" means.
func forest() fake {
	return fake{"null": "", "A": "null", "B": "A", "C": "A", "D": "C"}
}

func open(t *testing.T) (*Tree, fake) {
	t.Helper()
	f := forest()
	x, err := Open(t.TempDir(), f)
	if err != nil {
		t.Fatal(err)
	}
	return x, f
}

// Before any promote the two trees agree, so a trunkful figaro behaves
// exactly like a trunkless one.
func TestFallsThroughToTopology(t *testing.T) {
	x, _ := open(t)
	if !x.Normalized() {
		t.Error("a fresh tree must be normalized")
	}
	if p, _ := x.Parent("D"); p != "C" {
		t.Errorf("Parent(D) = %q, want C", p)
	}
}

// Promote edits presentation and NOTHING else: the topology still says B
// inherits from A, which is what keeps its history readable.
func TestPromoteMovesPresentationOnly(t *testing.T) {
	x, f := open(t)
	if err := x.Promote("B"); err != nil {
		t.Fatal(err)
	}
	if p, _ := x.Parent("B"); p != "null" {
		t.Errorf("presentation Parent(B) = %q, want the null root", p)
	}
	if p, _ := x.Parent("A"); p != "B" {
		t.Errorf("presentation Parent(A) = %q, want B", p)
	}
	if up, _ := f.From("B"); up != "A" {
		t.Errorf("TOPOLOGY moved: From(B) = %q, want A", up)
	}
	if x.Normalized() {
		t.Error("a promoted tree is not normalized")
	}
}

// The whole reason boundary repair exists. After promoting B, deleting A
// takes C and D but NOT B — and B still inherits A's prefix.
func TestPromoteOpensABoundary(t *testing.T) {
	x, f := open(t)
	if err := x.Promote("B"); err != nil {
		t.Fatal(err)
	}
	del := x.DeleteSet("A")
	sort.Strings(del)
	if len(del) != 3 || del[0] != "A" || del[1] != "C" || del[2] != "D" {
		t.Fatalf("DeleteSet(A) = %v, want [A C D]", del)
	}
	b := topo.Boundary(f, del)
	if len(b) != 1 || b[0] != "B" {
		t.Fatalf("Boundary = %v, want [B]", b)
	}
}

func TestOverridesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	f := forest()
	x, err := Open(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Promote("B"); err != nil {
		t.Fatal(err)
	}
	y, err := Open(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := y.Parent("A"); p != "B" {
		t.Errorf("after reopen Parent(A) = %q, want B", p)
	}
}

// Reparenting back to the topology edge drops the override rather than
// storing a redundant one, so a normalized tree is genuinely empty on disk.
func TestReparentToTopologyClearsTheOverride(t *testing.T) {
	x, _ := open(t)
	if err := x.Promote("B"); err != nil {
		t.Fatal(err)
	}
	if err := x.Reparent("B", "A"); err != nil {
		t.Fatal(err)
	}
	if err := x.Reparent("A", ""); err != nil {
		t.Fatal(err)
	}
	if !x.Normalized() {
		t.Error("tree must be normalized once every override matches topology")
	}
}

// Promoting an aria back to where its history puts it must leave NO
// override. Otherwise Normalized() stays false forever and every later
// delete pays to repair a boundary that is already empty.
func TestPromoteBackToTopologyClearsTheOverride(t *testing.T) {
	x, _ := open(t)
	if err := x.Promote("D"); err != nil { // D: C -> A, C under D
		t.Fatal(err)
	}
	if x.Normalized() {
		t.Fatal("a promoted tree is not normalized")
	}
	// Put it back: D under C, C under A again.
	if err := x.Promote("C"); err != nil {
		t.Fatal(err)
	}
	if p, _ := x.Parent("D"); p != "C" {
		t.Fatalf("Parent(D) = %q, want C", p)
	}
	if p, _ := x.Parent("C"); p != "A" {
		t.Fatalf("Parent(C) = %q, want A", p)
	}
	if !x.Normalized() {
		t.Error("a tree back in agreement with history must be normalized")
	}
}
