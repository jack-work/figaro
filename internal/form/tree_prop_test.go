package form

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// The value pool deliberately mixes:
//   - distinct values,
//   - values that differ in bytes but are semantically equal (reordered object
//     keys, whitespace), which must NOT read as changes,
//   - numbers that differ only in spelling (1 vs 1.0), which MUST read as
//     changes under the documented literal-token rule,
//   - bytes that are not JSON at all, which fall back to byte equality.
var propValues = []string{
	`1`,
	`1.0`,
	`"a"`,
	`null`,
	`true`,
	`[]`,
	`[1,2,3]`,
	`{"x":1,"y":2}`,
	`{"y":2,"x":1}`,
	`{ "x" : 1 , "y" : 2 }`,
	`{"x":1,"y":3}`,
	`{"nested":{"b":[1,{"q":0,"p":1}],"a":null}}`,
	`{"nested":{"a":null,"b":[1,{"p":1,"q":0}]}}`,
	`not json at all`,
	`{`,
	``,
}

func propKey(r *rand.Rand, n int) string { return fmt.Sprintf("k%03d", r.IntN(n)) }

func propValue(r *rand.Rand) Value {
	return NewValue(json.RawMessage(propValues[r.IntN(len(propValues))]))
}

// oracle is a plain map with the same update rule as ptree.Set: a
// semantically-equal write keeps the bytes already stored.
type oracle map[string]Value

func (o oracle) set(k string, v Value) {
	if old, ok := o[k]; ok && old.Equal(v) {
		return
	}
	o[k] = v
}

func (o oracle) clone() oracle {
	out := make(oracle, len(o))
	for k, v := range o {
		out[k] = v
	}
	return out
}

// checkAgainstOracle asserts the tree and the map agree on every operation.
func checkAgainstOracle(t *testing.T, tree ptree, want oracle, seed uint64) {
	t.Helper()
	if tree.Len() != len(want) {
		t.Fatalf("seed %d: Len() = %d, oracle has %d", seed, tree.Len(), len(want))
	}
	for k, v := range want {
		got, ok := tree.Get(k)
		if !ok {
			t.Fatalf("seed %d: missing key %q", seed, k)
		}
		if got.String() != v.String() {
			t.Fatalf("seed %d: key %q = %s, oracle has %s", seed, k, got, v)
		}
		if !tree.Has(k) {
			t.Fatalf("seed %d: Has(%q) false", seed, k)
		}
	}
	keys := treeKeys(tree)
	if !slices.IsSorted(keys) {
		t.Fatalf("seed %d: All() not sorted: %v", seed, keys)
	}
	if len(keys) != len(want) {
		t.Fatalf("seed %d: All() yielded %d keys, oracle has %d", seed, len(keys), len(want))
	}
	for _, k := range keys {
		if _, ok := want[k]; !ok {
			t.Fatalf("seed %d: All() yielded unknown key %q", seed, k)
		}
	}
}

func TestPropertyTreeMatchesMapOracle(t *testing.T) {
	rounds, ops := 300, 400
	if testing.Short() {
		rounds, ops = 20, 100
	}
	for round := range rounds {
		seed := uint64(round)*7919 + 1
		r := rand.New(rand.NewPCG(seed, 0xfeed))
		keyspace := 1 + r.IntN(64)

		tree := ptree{}
		want := oracle{}
		// A few historical versions, to prove persistence: each must still
		// match the oracle it was taken with, however much the tree moved on.
		type version struct {
			tree ptree
			want oracle
		}
		var history []version

		for range ops {
			switch r.IntN(10) {
			case 0, 1, 2, 3, 4, 5: // set
				k, v := propKey(r, keyspace), propValue(r)
				tree = tree.Set(k, v)
				want.set(k, v)
			case 6, 7, 8: // delete
				k := propKey(r, keyspace)
				before := tree.root
				tree = tree.Delete(k)
				if _, existed := want[k]; !existed && tree.root != before {
					t.Fatalf("seed %d: deleting absent %q changed the root", seed, k)
				}
				delete(want, k)
			default: // snapshot the current version
				history = append(history, version{tree, want.clone()})
			}
			assertTreeInvariants(t, tree)
			if tree.Len() != len(want) {
				t.Fatalf("seed %d: Len() = %d, oracle has %d", seed, tree.Len(), len(want))
			}
		}

		checkAgainstOracle(t, tree, want, seed)
		for i, v := range history {
			assertTreeInvariants(t, v.tree)
			checkAgainstOracle(t, v.tree, v.want, seed)
			if v.tree.Len() != len(v.want) {
				t.Fatalf("seed %d: history[%d] drifted", seed, i)
			}
		}
	}
}

func TestPropertyDiffMatchesNaiveDiff(t *testing.T) {
	rounds := 400
	if testing.Short() {
		rounds = 40
	}
	for round := range rounds {
		seed := uint64(round)*104729 + 3
		r := rand.New(rand.NewPCG(seed, 0xd1ff))
		keyspace := 1 + r.IntN(48)

		// prev is built from scratch; next is derived from prev by a random
		// edit sequence (the sharing-rich case) half the time, and built
		// independently (the sharing-free case) the other half.
		prev := ptree{}
		for range r.IntN(60) {
			prev = prev.Set(propKey(r, keyspace), propValue(r))
		}
		next := prev
		if round%2 == 1 {
			next = ptree{}
			for range r.IntN(60) {
				next = next.Set(propKey(r, keyspace), propValue(r))
			}
		}
		for range r.IntN(20) {
			if r.IntN(3) == 0 {
				next = next.Delete(propKey(r, keyspace))
			} else {
				next = next.Set(propKey(r, keyspace), propValue(r))
			}
		}

		got := diffTrees(prev.root, next.root)
		want := naiveDiff(prev, next)
		if !patchesEqual(got, want) {
			t.Fatalf("seed %d:\n diffTrees = %s\n naiveDiff = %s", seed, patchString(got), patchString(want))
		}
		if !slices.IsSorted(got.Remove) {
			t.Fatalf("seed %d: Remove not sorted: %v", seed, got.Remove)
		}
		if err := checkPatchCarries(prev, next, got); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		// The diff is read-only.
		assertTreeInvariants(t, prev)
		assertTreeInvariants(t, next)
		// And the reverse direction is a valid patch too.
		back := diffTrees(next.root, prev.root)
		if err := checkPatchCarries(next, prev, back); err != nil {
			t.Fatalf("seed %d (reverse): %v", seed, err)
		}
	}
}

func TestPropertyDiffOfIdenticalContentIsEmpty(t *testing.T) {
	rounds := 200
	if testing.Short() {
		rounds = 20
	}
	for round := range rounds {
		seed := uint64(round)*15485863 + 11
		r := rand.New(rand.NewPCG(seed, 0x5a5a))
		keys := make([]string, 0, 40)
		vals := make([]Value, 0, 40)
		for i := range 1 + r.IntN(40) {
			keys = append(keys, fmt.Sprintf("k%03d", i))
			vals = append(vals, propValue(r))
		}

		// Insert the same bindings in two different random orders: same
		// content, generally different shapes, no shared pointers.
		build := func() ptree {
			order := r.Perm(len(keys))
			var tree ptree
			for _, i := range order {
				tree = tree.Set(keys[i], vals[i])
			}
			return tree
		}
		a, b := build(), build()
		if p := diffTrees(a.root, b.root); !p.IsEmpty() {
			t.Fatalf("seed %d: same content diffed non-empty: %s", seed, patchString(p))
		}
		if p := diffTrees(b.root, a.root); !p.IsEmpty() {
			t.Fatalf("seed %d: same content diffed non-empty (reversed): %s", seed, patchString(p))
		}
	}
}

// Equality must be a proper equivalence relation across the whole value pool,
// including the not-JSON entries: anything else makes the diff order-dependent.
func TestPropertyValueEqualIsAnEquivalence(t *testing.T) {
	vals := make([]Value, 0, len(propValues))
	for _, s := range propValues {
		vals = append(vals, NewValue(json.RawMessage(s)))
	}
	for i, a := range vals {
		if !a.Equal(a) {
			t.Fatalf("%q is not equal to itself", propValues[i])
		}
		for j, b := range vals {
			if a.Equal(b) != b.Equal(a) {
				t.Fatalf("asymmetric: %q vs %q", propValues[i], propValues[j])
			}
			if !a.Equal(b) {
				continue
			}
			for k, c := range vals {
				if b.Equal(c) && !a.Equal(c) {
					t.Fatalf("intransitive: %q == %q == %q", propValues[i], propValues[j], propValues[k])
				}
			}
		}
	}
}
