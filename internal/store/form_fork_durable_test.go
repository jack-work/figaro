package store

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func stateOf(t *testing.T, be *XwalBackend, id string) map[string]string {
	t.Helper()
	snap, err := be.FormState(id)
	if err != nil {
		t.Fatalf("form state of %s: %v", id, err)
	}
	out := map[string]string{}
	for k, v := range snap.All() {
		out[k] = string(v)
	}
	return out
}

func dropOf(keys ...string) form.Patch {
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{}, keys)
}

func mustFork(t *testing.T, be *XwalBackend, parent string, kv map[string]string) string {
	t.Helper()
	id, _, err := be.CreateForm(parent, patchOf(t, kv))
	if err != nil {
		t.Fatalf("fork %q: %v", parent, err)
	}
	return id
}

func mustPatch(t *testing.T, be *XwalBackend, id string, p form.Patch) {
	t.Helper()
	if _, err := be.ApplyForm(id, p); err != nil {
		t.Fatalf("patch %s: %v", id, err)
	}
}

type forkChain struct {
	parent, child, grandchild string
	want                      map[string]map[string]string
}

// parent ── child ── grandchild, every link patched on both sides after it
// was cut, and a deletion carried down the chain.
func buildForkChain(t *testing.T, be *XwalBackend) forkChain {
	t.Helper()
	c := forkChain{}
	c.parent = mustFork(t, be, "", map[string]string{"k0": `0`, "shared": `"parent"`, "nest.a": `1`, "nest.b": `2`})
	c.child = mustFork(t, be, c.parent, map[string]string{"who": `"child"`})
	mustPatch(t, be, c.parent, patchOf(t, map[string]string{"after.parent": `true`, "shared": `"parent, later"`}))
	mustPatch(t, be, c.child, patchOf(t, map[string]string{"after.child": `true`, "nest.a": `10`}))
	mustPatch(t, be, c.child, dropOf("nest.b"))
	c.grandchild = mustFork(t, be, c.child, map[string]string{"who": `"grandchild"`})
	mustPatch(t, be, c.child, patchOf(t, map[string]string{"after.grand": `"not seen below"`}))
	mustPatch(t, be, c.grandchild, dropOf("k0"))
	c.want = map[string]map[string]string{
		c.parent:     {"k0": `0`, "shared": `"parent, later"`, "nest.a": `1`, "nest.b": `2`, "after.parent": `true`},
		c.child:      {"k0": `0`, "shared": `"parent"`, "nest.a": `10`, "who": `"child"`, "after.child": `true`, "after.grand": `"not seen below"`},
		c.grandchild: {"shared": `"parent"`, "nest.a": `10`, "who": `"grandchild"`, "after.child": `true`},
	}
	return c
}

func (c forkChain) check(t *testing.T, be *XwalBackend, when string) {
	t.Helper()
	for _, id := range []string{c.parent, c.child, c.grandchild} {
		if got := stateOf(t, be, id); !maps.Equal(got, c.want[id]) {
			t.Errorf("%s: %s\n got %v\nwant %v", when, id, got, c.want[id])
		}
	}
}

// A fork is a copy at the moment it is cut: no later patch on either side
// crosses the link, a deletion in the child stays in the child, and a fork
// of a fork copies the child as it stood, not the root.
func TestAFormForkCopiesAtTheCutAndNothingCrossesAfter(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	c := buildForkChain(t, be)
	c.check(t, be, "live")
	for id, want := range map[string]string{c.child: c.parent, c.grandchild: c.child} {
		if n, ok := be.Node(id); !ok || n.Parent != want {
			t.Errorf("%s: parent %q ok=%v, want %s", id, n.Parent, ok, want)
		}
	}
}

// Everything a fork chain holds is what the store reads back after a restart.
func TestAFormForkChainReadsTheSameAfterARestart(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := buildForkChain(t, be)
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	be, err = NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	c.check(t, be, "after restart")
	mustPatch(t, be, c.parent, patchOf(t, map[string]string{"post.restart": `1`}))
	if got := stateOf(t, be, c.child)["post.restart"]; got != "" {
		t.Errorf("a parent patch after the restart reached the child: %s", got)
	}
}

// A form with forks under it is not deleted without -r, so no fork is lost
// by deleting what it was cut from; once its forks are gone it deletes, and
// what remains reads the same after a restart.
func TestAFormIsNotDeletedFromUnderItsForks(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := buildForkChain(t, be)
	for _, id := range []string{c.parent, c.child} {
		if err := be.Remove(id, false); err == nil {
			t.Fatalf("removed %s from under its fork", id)
		}
	}
	c.check(t, be, "after the refused deletes")
	if err := be.Remove(c.grandchild, false); err != nil {
		t.Fatalf("remove the leaf: %v", err)
	}
	if err := be.Remove(c.child, false); err != nil {
		t.Fatalf("remove the middle once its fork is gone: %v", err)
	}
	for _, id := range []string{c.child, c.grandchild} {
		if _, err := be.FormState(id); err == nil {
			t.Errorf("removed form %s still reads", id)
		}
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	if be, err = NewXwalBackend(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	if got := stateOf(t, be, c.parent); !maps.Equal(got, c.want[c.parent]) {
		t.Errorf("parent after its forks were deleted and a restart\n got %v\nwant %v", got, c.want[c.parent])
	}
	for _, id := range []string{c.child, c.grandchild} {
		if _, ok := be.Node(id); ok {
			t.Errorf("removed form %s came back after a restart", id)
		}
	}
}
