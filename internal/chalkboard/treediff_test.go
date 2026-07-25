package chalkboard

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// diffWithStats is diffTrees with the differ's work counters exposed, so tests
// can prove the pointer short-circuit rather than take it on faith.
func diffWithStats(prev, next *node) (Patch, differ) {
	var d differ
	d.walk(prev, next)
	slices.Sort(d.patch.Remove)
	return d.patch, d
}

func patchString(p Patch) string {
	keys := slices.Sorted(maps.Keys(p.Set))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, p.Set[k]))
	}
	return fmt.Sprintf("set{%v} remove%v", parts, p.Remove)
}

func patchesEqual(a, b Patch) bool {
	if len(a.Set) != len(b.Set) || !slices.Equal(a.Remove, b.Remove) {
		return false
	}
	for k, v := range a.Set {
		w, ok := b.Set[k]
		if !ok || string(v) != string(w) {
			return false
		}
	}
	return true
}

// naiveDiff is the reference implementation: the map-based Diff that lives on
// Snapshot today, with byte equality swapped for Value equality.
func naiveDiff(prev, next ptree) Patch {
	var p Patch
	for k, v := range next.All() {
		if old, ok := prev.Get(k); !ok || !old.Equal(v) {
			if p.Set == nil {
				p.Set = make(map[string]json.RawMessage)
			}
			p.Set[k] = v.Raw()
		}
	}
	for k := range prev.All() {
		if !next.Has(k) {
			p.Remove = append(p.Remove, k)
		}
	}
	slices.Sort(p.Remove)
	return p
}

func treeOf(t testing.TB, pairs map[string]string) ptree {
	t.Helper()
	var tree ptree
	for _, k := range slices.Sorted(maps.Keys(pairs)) {
		tree = tree.Set(k, mustValue(t, pairs[k]))
	}
	return tree
}

func TestDiffTreesCases(t *testing.T) {
	cases := []struct {
		name       string
		prev, next map[string]string
		set        map[string]string
		remove     []string
	}{
		{name: "both empty"},
		{
			name: "empty to full",
			next: map[string]string{"b": `2`, "a": `1`},
			set:  map[string]string{"a": `1`, "b": `2`},
		},
		{
			name:   "full to empty",
			prev:   map[string]string{"b": `2`, "a": `1`},
			remove: []string{"a", "b"},
		},
		{
			name: "identical",
			prev: map[string]string{"a": `1`, "b": `2`},
			next: map[string]string{"a": `1`, "b": `2`},
		},
		{
			name: "value changed",
			prev: map[string]string{"a": `1`, "b": `2`},
			next: map[string]string{"a": `1`, "b": `3`},
			set:  map[string]string{"b": `3`},
		},
		{
			name: "added and removed",
			prev: map[string]string{"a": `1`, "b": `2`, "c": `3`},
			next: map[string]string{"a": `1`, "d": `4`},
			set:  map[string]string{"d": `4`},
			// sorted, per the Snapshot.Diff contract
			remove: []string{"b", "c"},
		},
		{
			name: "key order in an object is not a change",
			prev: map[string]string{"a": `{"x":1,"y":2}`},
			next: map[string]string{"a": `{"y":2,"x":1}`},
		},
		{
			name: "whitespace is not a change",
			prev: map[string]string{"a": `{"x": 1}`},
			next: map[string]string{"a": `{"x":1}`},
		},
		{
			name: "1.0 is a change from 1",
			prev: map[string]string{"a": `1`},
			next: map[string]string{"a": `1.0`},
			set:  map[string]string{"a": `1.0`},
		},
		{
			name:   "disjoint key sets",
			prev:   map[string]string{"a": `1`, "c": `3`, "e": `5`},
			next:   map[string]string{"b": `2`, "d": `4`, "f": `6`},
			set:    map[string]string{"b": `2`, "d": `4`, "f": `6`},
			remove: []string{"a", "c", "e"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev, next := treeOf(t, tc.prev), treeOf(t, tc.next)
			want := Patch{Remove: tc.remove}
			if len(tc.set) > 0 {
				want.Set = make(map[string]json.RawMessage, len(tc.set))
				for k, v := range tc.set {
					want.Set[k] = json.RawMessage(v)
				}
			}
			got := diffTrees(prev.root, next.root)
			if !patchesEqual(got, want) {
				t.Fatalf("diffTrees = %s, want %s", patchString(got), patchString(want))
			}
			if ref := naiveDiff(prev, next); !patchesEqual(got, ref) {
				t.Fatalf("diffTrees = %s, naive = %s", patchString(got), patchString(ref))
			}
			if !slices.IsSorted(got.Remove) {
				t.Fatalf("Remove is not sorted: %v", got.Remove)
			}
			// The patch must actually carry prev to next.
			if err := checkPatchCarries(prev, next, got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// checkPatchCarries verifies that applying p to prev yields exactly next.
func checkPatchCarries(prev, next ptree, p Patch) error {
	got := prev
	for k, v := range p.Set {
		got = got.Set(k, NewValue(v))
	}
	for _, k := range p.Remove {
		got = got.Delete(k)
	}
	if got.Len() != next.Len() {
		return fmt.Errorf("applying %s gave %d keys, want %d", patchString(p), got.Len(), next.Len())
	}
	for k, want := range next.All() {
		have, ok := got.Get(k)
		if !ok {
			return fmt.Errorf("applying %s lost key %q", patchString(p), k)
		}
		if !have.Equal(want) {
			return fmt.Errorf("applying %s left %q = %s, want %s", patchString(p), k, have, want)
		}
	}
	return nil
}

func TestDiffTreesSetCarriesRawBytes(t *testing.T) {
	prev := treeOf(t, map[string]string{"a": `1`})
	next := prev.Set("a", mustValue(t, "{ \"x\":\n1 }"))
	p := diffTrees(prev.root, next.root)
	if got := string(p.Set["a"]); got != "{ \"x\":\n1 }" {
		t.Fatalf("patch carried %q, want the exact stored bytes", got)
	}
}

func TestDiffTreesShortCircuitsOnPointerIdentity(t *testing.T) {
	const n = 1024
	var tree ptree
	for i := range n {
		tree = tree.Set(fmt.Sprintf("k%04d", i), mustValue(t, fmt.Sprint(i)))
	}

	// Identical trees: one comparison, nothing walked.
	if p, d := diffWithStats(tree.root, tree.root); !p.IsEmpty() || d.visited != 0 || d.splits != 0 {
		t.Fatalf("self-diff: patch=%s visited=%d splits=%d", patchString(p), d.visited, d.splits)
	}

	// One key changed out of 1024: the diff must touch O(log n) nodes, not n.
	next := tree.Set("k0500", mustValue(t, `"changed"`))
	p, d := diffWithStats(tree.root, next.root)
	if len(p.Set) != 1 || p.Set["k0500"] == nil || len(p.Remove) != 0 {
		t.Fatalf("patch = %s", patchString(p))
	}
	t.Logf("1-key change in a %d-key tree: visited=%d shared=%d splits=%d", n, d.visited, d.shared, d.splits)
	if d.shared == 0 {
		t.Fatal("no subtree was skipped by pointer identity")
	}
	if d.visited > 32 { // AVL depth for n=1024 is <= 15
		t.Fatalf("visited %d node pairs for a single-key change in %d keys", d.visited, n)
	}

	// A 10-key change is still logarithmic, not linear.
	batched := tree
	for i := 0; i < 10; i++ {
		batched = batched.Set(fmt.Sprintf("k%04d", i*100), mustValue(t, `"x"`))
	}
	p, d = diffWithStats(tree.root, batched.root)
	t.Logf("10-key change in a %d-key tree: visited=%d shared=%d splits=%d", n, d.visited, d.shared, d.splits)
	if len(p.Set) != 10 {
		t.Fatalf("patch = %s", patchString(p))
	}
	if d.visited > 10*32 {
		t.Fatalf("visited %d node pairs for a 10-key change in %d keys", d.visited, n)
	}
}

// The short-circuit is an optimisation layered over a merge-join; correctness
// must not depend on it. Rebuilding an identical tree from scratch shares no
// pointers at all, so this exercises the fully general path.
func TestDiffTreesCorrectWithoutSharing(t *testing.T) {
	build := func(order []int, changed map[int]string) ptree {
		var tree ptree
		for _, i := range order {
			v := fmt.Sprint(i)
			if c, ok := changed[i]; ok {
				v = c
			}
			tree = tree.Set(fmt.Sprintf("k%03d", i), mustValue(t, v))
		}
		return tree
	}
	ascending := make([]int, 200)
	descending := make([]int, 200)
	for i := range ascending {
		ascending[i] = i
		descending[i] = 199 - i
	}

	// Same content, opposite insertion orders: different shapes, zero sharing.
	prev, next := build(ascending, nil), build(descending, nil)
	if prev.root == next.root {
		t.Fatal("test is vacuous: the trees are the same object")
	}
	if p, d := diffWithStats(prev.root, next.root); !p.IsEmpty() {
		t.Fatalf("differently shaped identical trees diffed non-empty: %s (visited=%d)", patchString(p), d.visited)
	}

	// Now with a handful of real changes mixed in.
	next = build(descending, map[int]string{3: `"three"`, 100: `"hundred"`, 199: `"last"`})
	next = next.Delete("k007").Delete("k150")
	got := diffTrees(prev.root, next.root)
	want := naiveDiff(prev, next)
	if !patchesEqual(got, want) {
		t.Fatalf("diffTrees = %s\nnaiveDiff = %s", patchString(got), patchString(want))
	}
	if err := checkPatchCarries(prev, next, got); err != nil {
		t.Fatal(err)
	}
}

func TestDiffTreesSplitLeavesTreesIntact(t *testing.T) {
	// splitNode builds scratch nodes; nothing it touches may be mutated.
	prev := treeOf(t, map[string]string{"a": `1`, "b": `2`, "c": `3`, "d": `4`, "e": `5`})
	next := treeOf(t, map[string]string{"b": `9`, "c": `3`, "z": `26`})
	before := patchString(naiveDiff(ptree{}, prev)) + "|" + patchString(naiveDiff(ptree{}, next))
	rootPrev, rootNext := prev.root, next.root
	diffTrees(prev.root, next.root)
	if prev.root != rootPrev || next.root != rootNext {
		t.Fatal("diff replaced a root pointer")
	}
	if after := patchString(naiveDiff(ptree{}, prev)) + "|" + patchString(naiveDiff(ptree{}, next)); after != before {
		t.Fatalf("diff mutated a tree:\nbefore %s\nafter  %s", before, after)
	}
	assertTreeInvariants(t, prev)
	assertTreeInvariants(t, next)
}
