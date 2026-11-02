package form

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

func nodeKeys(n Node) []string {
	var out []string
	for c := range n.Children() {
		out = append(out, c.Key())
	}
	return out
}

func child(t *testing.T, n Node, key string) Node {
	t.Helper()
	for c := range n.Children() {
		if c.Key() == key {
			return c
		}
	}
	t.Fatalf("no child %q under %q (have %v)", key, n.Key(), nodeKeys(n))
	return Node{}
}

// The view reuses a rendered row while its node's stamp holds, so an
// untouched subtree must keep its stamp across an Apply, and a touched one
// must not.
func TestNodeStampsSurviveApplyOnlyWhereUntouched(t *testing.T) {
	keys := map[string]json.RawMessage{}
	for i := range 40 {
		keys[fmt.Sprintf("k%02d.leaf", i)] = json.RawMessage(fmt.Sprintf(`%d`, i))
		keys[fmt.Sprintf("k%02d.deep.x", i)] = json.RawMessage(`"x"`)
	}
	before := Snapshot{}.Apply(Build(Snapshot{}, keys, nil))
	after := before.Apply(Build(before, map[string]json.RawMessage{"k17.deep.x": json.RawMessage(`"y"`)}, nil))

	if before.RootNode().Stamp() == after.RootNode().Stamp() {
		t.Fatal("the root kept its stamp through a change")
	}
	for i := range 40 {
		key := fmt.Sprintf("k%02d", i)
		b, a := child(t, before.RootNode(), key), child(t, after.RootNode(), key)
		same := b.Stamp() == a.Stamp()
		if i == 17 && same {
			t.Fatalf("%s changed and kept its stamp", key)
		}
		if i != 17 && !same {
			t.Fatalf("%s is untouched and lost its stamp (a sibling's rebalance must not count)", key)
		}
	}
	b17, a17 := child(t, before.RootNode(), "k17"), child(t, after.RootNode(), "k17")
	if child(t, b17, "leaf").Stamp() != child(t, a17, "leaf").Stamp() {
		t.Fatal("an untouched leaf beside the change lost its stamp")
	}
	if child(t, b17, "deep").Stamp() == child(t, a17, "deep").Stamp() {
		t.Fatal("the changed branch kept its stamp")
	}
}

func TestNodeChildrenAreSortedAndDottedKeysStayWhole(t *testing.T) {
	s := FromMap(map[string]json.RawMessage{"b": json.RawMessage(`1`), "a": json.RawMessage(`2`)})
	s = s.Apply(Patch{Object: &ObjectPatch{Set: map[string]Value{"m": NewValue(json.RawMessage(`{"x.y":1,"c":{"d":2}}`))}}})
	if got := nodeKeys(s.RootNode()); !slices.Equal(got, []string{"a", "b", "m"}) {
		t.Fatalf("top level = %v", got)
	}
	m := child(t, s.RootNode(), "m")
	if !m.Branch() || m.Len() != 2 {
		t.Fatalf("m: branch=%v len=%d", m.Branch(), m.Len())
	}
	if got := nodeKeys(m); !slices.Equal(got, []string{"c", "x.y"}) {
		t.Fatalf("m's keys = %v", got)
	}
	raw, err := m.Encode()
	if err != nil || string(raw) != `{"c":{"d":2},"x.y":1}` {
		t.Fatalf("m encodes as %s (%v)", raw, err)
	}
}

func TestNodeEmptyObjectIsALeaf(t *testing.T) {
	s := FromMap(map[string]json.RawMessage{"e": json.RawMessage(`{}`), "n": json.RawMessage(`null`), "s": json.RawMessage(`"x"`)})
	e := child(t, s.RootNode(), "e")
	if e.Branch() {
		t.Fatal("an empty object reads as a branch with nothing under it")
	}
	if got := e.Value().String(); got != "{}" {
		t.Fatalf("empty object value = %s", got)
	}
	if v := child(t, s.RootNode(), "s").Value().String(); v != `"x"` {
		t.Fatalf("leaf value = %s", v)
	}
}

func TestNodeEmptySnapshot(t *testing.T) {
	var s Snapshot
	if s.RootNode().Branch() || s.RootNode().Len() != 0 || len(nodeKeys(s.RootNode())) != 0 {
		t.Fatal("the empty snapshot has children")
	}
}
