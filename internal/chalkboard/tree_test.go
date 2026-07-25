package chalkboard

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"testing"
)

// assertTreeInvariants checks everything that must hold of a ptree after any
// mutation: keys in order, AVL balance within 1, and cached height/size exact.
// The property tests call this after every single operation, not just at the end.
func assertTreeInvariants(t testing.TB, tree ptree) {
	t.Helper()

	var keys []string
	tree.Range(func(k string, _ Value) bool { keys = append(keys, k); return true })
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("in-order traversal is not sorted: %v", keys)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			t.Fatalf("duplicate key %q in tree", keys[i])
		}
	}

	var walk func(n *node) (height, size int)
	walk = func(n *node) (int, int) {
		if n == nil {
			return 0, 0
		}
		lh, ls := walk(n.left)
		rh, rs := walk(n.right)
		if d := lh - rh; d < -1 || d > 1 {
			t.Fatalf("AVL balance violated at %q: %d", n.key, d)
		}
		if want := 1 + max(lh, rh); n.height != want {
			t.Fatalf("height at %q is %d, want %d", n.key, n.height, want)
		}
		if want := 1 + ls + rs; n.size != want {
			t.Fatalf("size at %q is %d, want %d", n.key, n.size, want)
		}
		if n.left != nil && n.left.key >= n.key {
			t.Fatalf("BST order violated: %q left of %q", n.left.key, n.key)
		}
		if n.right != nil && n.right.key <= n.key {
			t.Fatalf("BST order violated: %q right of %q", n.right.key, n.key)
		}
		return n.height, n.size
	}
	_, size := walk(tree.root)
	if size != tree.Len() {
		t.Fatalf("Len() = %d, walked size = %d", tree.Len(), size)
	}
	if size != len(keys) {
		t.Fatalf("walked size = %d, traversal yielded %d keys", size, len(keys))
	}
}

func treeKeys(tree ptree) []string {
	return slices.Collect(func(yield func(string) bool) {
		for k := range tree.All() {
			if !yield(k) {
				return
			}
		}
	})
}

func TestTreeZeroValueIsEmpty(t *testing.T) {
	var tree ptree
	assertTreeInvariants(t, tree)
	if tree.Len() != 0 {
		t.Fatalf("Len() = %d", tree.Len())
	}
	if _, ok := tree.Get("nope"); ok {
		t.Fatal("Get found a key in an empty tree")
	}
	if tree.Has("nope") {
		t.Fatal("Has true on an empty tree")
	}
	if got := tree.Delete("nope"); got.root != nil {
		t.Fatal("Delete on empty tree produced a root")
	}
	for range tree.All() {
		t.Fatal("All yielded on an empty tree")
	}
	tree.Range(func(string, Value) bool { t.Fatal("Range yielded on an empty tree"); return false })
}

func TestTreeSingleNode(t *testing.T) {
	tree := ptree{}.Set("a", mustValue(t, `1`))
	assertTreeInvariants(t, tree)
	if tree.Len() != 1 || !tree.Has("a") {
		t.Fatalf("Len=%d Has=%v", tree.Len(), tree.Has("a"))
	}
	v, ok := tree.Get("a")
	if !ok || v.String() != "1" {
		t.Fatalf("Get = %q %v", v, ok)
	}
	if _, ok := tree.Get("`"); ok { // key just below "a"
		t.Fatal("phantom key below")
	}
	if _, ok := tree.Get("b"); ok {
		t.Fatal("phantom key above")
	}
	empty := tree.Delete("a")
	assertTreeInvariants(t, empty)
	if empty.Len() != 0 || empty.root != nil {
		t.Fatalf("delete left %d entries", empty.Len())
	}
	if tree.Len() != 1 {
		t.Fatal("Delete mutated the receiver")
	}
}

func TestTreeSetGetDelete(t *testing.T) {
	keys := []string{"d", "b", "f", "a", "c", "e", "g"}
	var tree ptree
	for i, k := range keys {
		tree = tree.Set(k, mustValue(t, fmt.Sprint(i)))
		assertTreeInvariants(t, tree)
	}
	if got, want := treeKeys(tree), []string{"a", "b", "c", "d", "e", "f", "g"}; !slices.Equal(got, want) {
		t.Fatalf("All order = %v, want %v", got, want)
	}
	if tree.Len() != 7 {
		t.Fatalf("Len = %d", tree.Len())
	}

	updated := tree.Set("c", mustValue(t, `"new"`))
	assertTreeInvariants(t, updated)
	if updated.Len() != 7 {
		t.Fatalf("update changed Len to %d", updated.Len())
	}
	if v, _ := tree.Get("c"); v.String() != "4" {
		t.Fatalf("original tree observed the update: %s", v)
	}
	if v, _ := updated.Get("c"); v.String() != `"new"` {
		t.Fatalf("update not visible: %s", v)
	}

	// Deleting an internal node with two children (the root) must promote the
	// in-order successor and keep everything else reachable.
	pruned := tree.Delete("d")
	assertTreeInvariants(t, pruned)
	if got, want := treeKeys(pruned), []string{"a", "b", "c", "e", "f", "g"}; !slices.Equal(got, want) {
		t.Fatalf("after delete: %v, want %v", got, want)
	}
	if !tree.Has("d") {
		t.Fatal("Delete mutated the receiver")
	}
	if same := pruned.Delete("d"); same.root != pruned.root {
		t.Fatal("deleting an absent key allocated a new root")
	}
}

func TestTreeSetSemanticNoOpKeepsIdentity(t *testing.T) {
	var tree ptree
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		tree = tree.Set(k, mustValue(t, `{"x":1,"y":2}`))
	}
	// Reordered object keys are the same value: no new nodes, same root.
	same := tree.Set("c", mustValue(t, `{"y":2,"x":1}`))
	if same.root != tree.root {
		t.Fatal("semantically identical Set copied the path")
	}
	// ...and the ORIGINAL bytes are kept, so nothing user-visible shifts.
	if v, _ := same.Get("c"); v.String() != `{"x":1,"y":2}` {
		t.Fatalf("raw bytes were replaced: %s", v)
	}
	// A real change still copies.
	changed := tree.Set("c", mustValue(t, `{"x":2,"y":2}`))
	if changed.root == tree.root {
		t.Fatal("a real change did not copy")
	}
}

func TestTreeStructuralSharing(t *testing.T) {
	var tree ptree
	for _, k := range []string{"d", "b", "f", "a", "c", "e", "g"} {
		tree = tree.Set(k, mustValue(t, `1`))
	}
	// Root is "d"; touching "c" must copy only d -> b -> c.
	next := tree.Set("c", mustValue(t, `2`))

	if next.root == tree.root {
		t.Fatal("no new root")
	}
	if next.root.right != tree.root.right {
		t.Fatal("untouched right subtree ('f') was copied")
	}
	if next.root.left == tree.root.left {
		t.Fatal("the path to 'c' was not copied")
	}
	if next.root.left.left != tree.root.left.left {
		t.Fatal("untouched sibling 'a' was copied")
	}
	if v, _ := tree.Get("c"); v.String() != "1" {
		t.Fatalf("original tree mutated: %s", v)
	}
	if v, _ := next.Get("c"); v.String() != "2" {
		t.Fatalf("new tree missing the write: %s", v)
	}

	// Count it, too: a single Set into a 1024-key tree must allocate only a
	// logarithmic number of fresh nodes.
	var big ptree
	for i := range 1024 {
		big = big.Set(fmt.Sprintf("k%04d", i), mustValue(t, fmt.Sprint(i)))
	}
	after := big.Set("k0500", mustValue(t, `"changed"`))
	fresh := countFreshNodes(big.root, after.root)
	if fresh > 24 { // depth of a 1024-node AVL tree is <= 15
		t.Fatalf("Set copied %d nodes, expected O(log n)", fresh)
	}
	t.Logf("Set into a %d-key tree allocated %d fresh nodes", big.Len(), fresh)
}

// countFreshNodes counts nodes reachable in next that are not pointer-shared
// with prev.
func countFreshNodes(prev, next *node) int {
	seen := map[*node]bool{}
	var mark func(*node)
	mark = func(n *node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		mark(n.left)
		mark(n.right)
	}
	mark(prev)
	count := 0
	var walk func(*node)
	walk = func(n *node) {
		if n == nil || seen[n] {
			return
		}
		count++
		walk(n.left)
		walk(n.right)
	}
	walk(next)
	return count
}

func TestTreeRangeEarlyStop(t *testing.T) {
	var tree ptree
	for i := range 10 {
		tree = tree.Set(fmt.Sprintf("k%d", i), mustValue(t, fmt.Sprint(i)))
	}
	var seen []string
	tree.Range(func(k string, _ Value) bool {
		seen = append(seen, k)
		return len(seen) < 3
	})
	if !slices.Equal(seen, []string{"k0", "k1", "k2"}) {
		t.Fatalf("Range ignored the stop signal: %v", seen)
	}

	seen = nil
	for k := range tree.All() {
		seen = append(seen, k)
		if len(seen) == 2 {
			break
		}
	}
	if !slices.Equal(seen, []string{"k0", "k1"}) {
		t.Fatalf("All ignored break: %v", seen)
	}
}

func TestTreeFromMap(t *testing.T) {
	m := map[string]json.RawMessage{
		"b": json.RawMessage(`2`),
		"a": json.RawMessage(`{"z":1,"y":2}`),
		"c": json.RawMessage(`null`),
	}
	tree := treeFromMap(m)
	assertTreeInvariants(t, tree)
	if got, want := treeKeys(tree), slices.Sorted(maps.Keys(m)); !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for k, raw := range m {
		v, ok := tree.Get(k)
		if !ok || v.String() != string(raw) {
			t.Fatalf("Get(%q) = %q %v, want %q", k, v, ok, raw)
		}
	}
	if treeFromMap(nil).Len() != 0 {
		t.Fatal("treeFromMap(nil) is not empty")
	}
}

func TestTreeBulkInsertDeleteStaysBalanced(t *testing.T) {
	var tree ptree
	const n = 2000
	for i := range n {
		tree = tree.Set(fmt.Sprintf("%05d", i), mustValue(t, fmt.Sprint(i)))
	}
	assertTreeInvariants(t, tree)
	if tree.Len() != n {
		t.Fatalf("Len = %d", tree.Len())
	}
	// Sorted insertion is the worst case for a naive BST; AVL must hold depth
	// near 1.44*log2(n) ~= 15.8 for n=2000.
	if h := nodeHeight(tree.root); h > 20 {
		t.Fatalf("height %d after %d sorted inserts", h, n)
	}
	for i := 0; i < n; i += 2 {
		tree = tree.Delete(fmt.Sprintf("%05d", i))
	}
	assertTreeInvariants(t, tree)
	if tree.Len() != n/2 {
		t.Fatalf("Len after deletes = %d", tree.Len())
	}
	for i := range n {
		k := fmt.Sprintf("%05d", i)
		if got := tree.Has(k); got != (i%2 == 1) {
			t.Fatalf("Has(%q) = %v", k, got)
		}
	}
}
