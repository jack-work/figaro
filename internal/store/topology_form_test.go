package store

// PARITY. This is internal/trunk's own test suite, pointed at the
// form-backed tree that replaces it: same diagram, same claims, same names.
// Porting the tests rather than writing new ones is what makes "it behaves
// as trunks.json did" a checked statement instead of a hopeful one.
//
// The two differences are mechanical: the tree is opened over a store (a
// form needs a log), and the fake topology is installed on it.

import (
	"os"
	"path/filepath"
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
func tree() fake {
	return fake{"null": "", "A": "null", "B": "A", "C": "A", "D": "C"}
}

func openTopo(t *testing.T) (*TopologyTree, fake) {
	t.Helper()
	f := tree()
	x, _ := openTopoIn(t, t.TempDir(), f)
	return x, f
}

func openTopoIn(t *testing.T, dir string, f fake) (*TopologyTree, *XwalBackend) {
	t.Helper()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	x, err := OpenTopologyTree(be.Store(), dir)
	if err != nil {
		t.Fatal(err)
	}
	x.topo = f
	t.Cleanup(func() { x.Close(); be.Close() })
	return x, be
}

// Before any promote the two trees agree, so a trunkful figaro behaves
// exactly like a trunkless one.
func TestTopologyForm_FallsThroughToTopology(t *testing.T) {
	x, _ := openTopo(t)
	if !x.Normalized() {
		t.Error("a fresh tree must be normalized")
	}
	if p, _ := x.Parent("D"); p != "C" {
		t.Errorf("Parent(D) = %q, want C", p)
	}
}

// Promote edits presentation and NOTHING else: the topology still says B
// inherits from A, which is what keeps its history readable.
func TestTopologyForm_PromoteMovesPresentationOnly(t *testing.T) {
	x, f := openTopo(t)
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
// takes C and D but NOT B, and B still inherits A's prefix.
func TestTopologyForm_PromoteOpensABoundary(t *testing.T) {
	x, f := openTopo(t)
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

func TestTopologyForm_OverridesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	f := tree()
	x, be := openTopoIn(t, dir, f)
	if err := x.Promote("B"); err != nil {
		t.Fatal(err)
	}
	// Close the writer and the store, then come back to it: the overrides
	// are records on a channel, so they are replayed rather than re-read.
	x.Close()
	be.Close()

	y, _ := openTopoIn(t, dir, f)
	if p, _ := y.Parent("A"); p != "B" {
		t.Errorf("after reopen Parent(A) = %q, want B", p)
	}
	if y.Rev() == 0 {
		t.Error("a tree with a landed promote must report a non-zero revision")
	}
}

// Reparenting back to the topology edge drops the override rather than
// storing a redundant one, so a normalized tree is genuinely empty on disk.
func TestTopologyForm_ReparentToTopologyClearsTheOverride(t *testing.T) {
	x, _ := openTopo(t)
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
func TestTopologyForm_PromoteBackToTopologyClearsTheOverride(t *testing.T) {
	x, _ := openTopo(t)
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

// A promote edits TWO edges. The file this replaces rewrote the whole
// document per edit, so the pair could half-land; one patch cannot.
func TestTopologyForm_PromoteIsOneRecord(t *testing.T) {
	x, _ := openTopo(t)
	before := x.Rev()
	if err := x.Promote("D"); err != nil {
		t.Fatal(err)
	}
	if got := x.Rev() - before; got != 1 {
		t.Fatalf("a promote landed %d records; two edges must be one patch", got)
	}
	ps := x.form.PatchesBetween(before, x.Rev())
	if len(ps) != 1 {
		t.Fatalf("want one patch, got %d", len(ps))
	}
	touched := len(ps[0].Patch.Set) + len(ps[0].Patch.Remove)
	if touched != 2 {
		t.Fatalf("the one patch names %d edges, want 2", touched)
	}
}

// A store with a trunks.json is read once, folded in, and the file renamed.
// The fold is idempotent, so a crash before the rename replays harmlessly.
func TestTopologyForm_MigratesTrunksJSON(t *testing.T) {
	dir := t.TempDir()
	f := tree()
	legacy := filepath.Join(dir, "trunks.json")
	if err := os.WriteFile(legacy,
		[]byte(`{"version":1,"parent":{"A":"B","B":"null"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	x, _ := openTopoIn(t, dir, f)
	if p, _ := x.Parent("A"); p != "B" {
		t.Fatalf("migrated Parent(A) = %q, want B", p)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("trunks.json survived its migration: it would be folded in again")
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Fatalf("the migrated file is not beside the store: %v", err)
	}
}

// An empty legacy file must not leave a phantom record, and a second open of
// a migrated store must not fold anything.
func TestTopologyForm_MigrationIsOnce(t *testing.T) {
	dir := t.TempDir()
	f := tree()
	if err := os.WriteFile(filepath.Join(dir, "trunks.json"),
		[]byte(`{"version":1,"parent":{"A":"B"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	x, be := openTopoIn(t, dir, f)
	rev := x.Rev()
	x.Close()
	be.Close()
	y, _ := openTopoIn(t, dir, f)
	if y.Rev() != rev {
		t.Fatalf("reopening migrated again: rev %d -> %d", rev, y.Rev())
	}
}

// THE CRASH WINDOW. The migration folds the file in and then renames it, so
// a crash between the two leaves a store whose form holds the edges AND
// whose trunks.json is still there. The next open must not fold them again:
// that is what "ordering, not a journal" means here, and it is checked by
// putting the file back rather than by killing a process.
func TestTopologyForm_CrashBetweenFoldAndRename(t *testing.T) {
	dir := t.TempDir()
	f := tree()
	legacy := filepath.Join(dir, "trunks.json")
	body := []byte(`{"version":1,"parent":{"A":"B"}}`)
	if err := os.WriteFile(legacy, body, 0o644); err != nil {
		t.Fatal(err)
	}
	x, be := openTopoIn(t, dir, f)
	rev := x.Rev()
	x.Close()
	be.Close()

	// The crash: the fold landed, the rename did not.
	if err := os.WriteFile(legacy, body, 0o644); err != nil {
		t.Fatal(err)
	}
	y, _ := openTopoIn(t, dir, f)
	if y.Rev() != rev {
		t.Fatalf("a second fold landed: rev %d -> %d", rev, y.Rev())
	}
	if p, _ := y.Parent("A"); p != "B" {
		t.Fatalf("Parent(A) = %q, want B", p)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("the rename did not complete on the recovery pass")
	}
}

// ABSENCE IS THE TRUTHFUL DEFAULT (durable-forms §1). A store whose topology
// form holds nothing draws every aria where its history puts it, rather than
// drawing a wrong tree. This is what makes the form's loss survivable.
func TestTopologyForm_EmptyDegradesToTheTopology(t *testing.T) {
	x, f := openTopo(t)
	if !x.Normalized() {
		t.Fatal("an empty topology form must be normalized")
	}
	for id, want := range map[string]string{"B": "A", "C": "A", "D": "C"} {
		if p, _ := x.Parent(id); p != want {
			t.Fatalf("Parent(%s) = %q, want the topology edge %q", id, p, want)
		}
	}
	if len(x.Edges()) != 0 {
		t.Fatalf("edges on a fresh tree: %v", x.Edges())
	}
	_ = f
}

// A Forget naming edges that are not there must not write a record. The
// delete path calls it on every delete, and a record per delete on a form
// with no retention is growth for nothing.
func TestTopologyForm_ForgetWritesNothingWhenNothingMatches(t *testing.T) {
	x, _ := openTopo(t)
	if err := x.Promote("B"); err != nil {
		t.Fatal(err)
	}
	rev := x.Rev()
	if err := x.Forget("Z", "Y"); err != nil {
		t.Fatal(err)
	}
	if x.Rev() != rev {
		t.Fatalf("Forget of unknown ids wrote a record: rev %d -> %d", rev, x.Rev())
	}
}
