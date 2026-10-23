package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func targetPatch(target string) form.Patch {
	if target == "" {
		return form.Build(form.Snapshot{}, nil, []string{targetAriaKey})
	}
	raw, _ := json.Marshal(target)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{targetAriaKey: raw}, nil)
}

// The sweep asks "does a role point into this subtree" every time something is
// due; answering it must never open a form.
func TestRoleIndexFollowsEveryWrite(t *testing.T) {
	b := newTTLBackend(t)
	role, _, err := b.CreateForm("", targetPatch("aria-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := b.RoleTargets(); got["aria-1"] != role {
		t.Fatalf("roles %v after create, want aria-1 -> %s", got, role)
	}
	if _, err := b.ApplyForm(role, targetPatch("aria-2")); err != nil {
		t.Fatal(err)
	}
	got, _ := b.RoleTargets()
	if got["aria-2"] != role || got["aria-1"] != "" {
		t.Fatalf("roles %v after recast, want only aria-2", got)
	}
	dup, _, err := b.CreateForm(role, form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": json.RawMessage(`"copy"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := b.RoleTargets(); got["aria-2"] == "" || (got["aria-2"] != role && got["aria-2"] != dup) {
		t.Fatalf("roles %v after duplicating the role; the copy points where its source did", got)
	}
	if _, err := b.ApplyForm(role, targetPatch("")); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(dup, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.RoleTargets(); len(got) != 0 {
		t.Fatalf("roles %v after uncasting one and deleting the other", got)
	}
}

func TestRoleIndexSeedsFromDiskAfterARestart(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	role, _, err := b.CreateForm("", targetPatch("aria-9"))
	if err != nil {
		t.Fatal(err)
	}
	b.Close()

	b2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if _, ready := b2.RoleTargets(); ready {
		t.Fatal("a fresh backend claims a complete role index before reading any form")
	}
	b2.SeedRoles(context.Background(), 0)
	got, ready := b2.RoleTargets()
	if !ready || got["aria-9"] != role {
		t.Fatalf("roles %v ready=%v after the seed, want aria-9 -> %s", got, ready, role)
	}
}
