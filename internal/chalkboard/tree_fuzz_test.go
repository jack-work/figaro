package chalkboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// FuzzValueCanonical drives arbitrary bytes through NewValue. Nothing here may
// panic, and for anything that parses the canonical form must be stable,
// idempotent, and blind to whitespace.
func FuzzValueCanonical(f *testing.F) {
	seeds := []string{
		``, `null`, `0`, `1`, `1.0`, `1e2`, `-0`, `"s"`, `[]`, `{}`,
		`{"a":1,"b":2}`, `{"b":2,"a":1}`, `{"a":1,"a":2}`, `[1,[2,[3]]]`,
		`{ "a" : [ 1 , 2 ] }`, `"\u003c"`, `"<"`, "\x00", `{`, `1 2`,
		`{"a":1} trailing`, `123456789012345678901234567890`,
		`{"":null}`, `"\ud800"`, "\xff\xfe",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		v := NewValue(json.RawMessage(data))

		// Raw is what was supplied (modulo the documented empty -> null).
		if len(data) == 0 {
			if v.String() != "null" {
				t.Fatalf("empty input became %q", v)
			}
		} else if !bytes.Equal(v.Raw(), data) {
			t.Fatalf("raw was rewritten: %q -> %q", data, v.Raw())
		}

		// Equality is reflexive and stable across construction.
		again := NewValue(json.RawMessage(append([]byte(nil), data...)))
		if !v.Equal(again) || !again.Equal(v) {
			t.Fatalf("%q is not equal to an identical value", data)
		}

		out, err := json.Marshal(v)
		if v.IsJSON() {
			if err != nil {
				t.Fatalf("MarshalJSON failed for %q: %v", data, err)
			}
			if !json.Valid(out) {
				t.Fatalf("valid value marshalled to invalid JSON: %q -> %q", data, out)
			}
		} else if err == nil {
			// encoding/json validates whatever MarshalJSON returns, so a Value
			// holding non-JSON bytes makes the enclosing document fail to
			// marshal. That matches today's map[string]json.RawMessage
			// behaviour exactly; it is asserted here so nobody "fixes" it by
			// silently substituting null.
			t.Fatalf("invalid value %q marshalled without error: %q", data, out)
		}

		if !v.IsJSON() {
			return
		}

		// The canonical form is itself valid JSON and canonicalises to itself.
		if !json.Valid(v.canonical()) {
			t.Fatalf("canonical form of %q is not valid JSON: %q", data, v.canonical())
		}
		c := NewValue(json.RawMessage(v.canonical()))
		if !c.IsJSON() || !bytes.Equal(c.canonical(), v.canonical()) {
			t.Fatalf("canonicalisation is not idempotent: %q -> %q -> %q", data, v.canonical(), c.canonical())
		}
		if !c.Equal(v) {
			t.Fatalf("value is not equal to its own canonical form: %q", data)
		}

		// Whitespace is not semantic: compacting must not change equality.
		if len(data) > 0 {
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				t.Fatalf("Compact failed on parseable input %q: %v", data, err)
			}
			if !NewValue(json.RawMessage(compact.Bytes())).Equal(v) {
				t.Fatalf("compaction changed the value: %q vs %q", data, compact.Bytes())
			}
		}
	})
}

// FuzzTreeOps drives a random Set/Delete sequence and checks the tree against a
// map oracle, the AVL invariants after every step, and diffTrees against the
// naive diff at the end.
func FuzzTreeOps(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7}, `{"a":1}`)
	f.Add([]byte{1, 1, 1, 1, 1, 1}, `{"b":2,"a":1}`)
	f.Add([]byte{0x10, 0x21, 0x32, 0x43, 0x54, 0x65, 0x76, 0x87, 0x98}, `null`)
	f.Add([]byte{}, ``)
	f.Add(bytes.Repeat([]byte{0xff}, 64), `not json`)

	f.Fuzz(func(t *testing.T, script []byte, extra string) {
		if len(script) > 4096 {
			script = script[:4096]
		}
		values := append(append([]string(nil), propValues...), extra)

		tree := ptree{}
		want := oracle{}
		var prev ptree

		for i := 0; i+1 < len(script); i += 2 {
			key := fmt.Sprintf("k%02d", int(script[i])%16)
			switch script[i] % 4 {
			case 0:
				tree = tree.Delete(key)
				delete(want, key)
			default:
				v := NewValue(json.RawMessage(values[int(script[i+1])%len(values)]))
				tree = tree.Set(key, v)
				want.set(key, v)
			}
			assertTreeInvariants(t, tree)
			if tree.Len() != len(want) {
				t.Fatalf("Len() = %d, oracle has %d", tree.Len(), len(want))
			}
			for k, v := range want {
				got, ok := tree.Get(k)
				if !ok || got.String() != v.String() {
					t.Fatalf("Get(%q) = %q %v, oracle has %q", k, got, ok, v)
				}
			}
			if !slices.IsSorted(treeKeys(tree)) {
				t.Fatalf("traversal not sorted: %v", treeKeys(tree))
			}
			if i == 0 {
				prev = tree
			}
		}

		got := diffTrees(prev.root, tree.root)
		if ref := naiveDiff(prev, tree); !patchesEqual(got, ref) {
			t.Fatalf("diffTrees = %s, naiveDiff = %s", patchString(got), patchString(ref))
		}
		if !slices.IsSorted(got.Remove) {
			t.Fatalf("Remove not sorted: %v", got.Remove)
		}
		if err := checkPatchCarries(prev, tree, got); err != nil {
			t.Fatal(err)
		}
	})
}
