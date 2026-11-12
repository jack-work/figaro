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
	if got, _ := b.roleTargetsForTest(); got["aria-1"] != role {
		t.Fatalf("roles %v after create, want aria-1 -> %s", got, role)
	}
	if _, err := b.ApplyForm(role, targetPatch("aria-2")); err != nil {
		t.Fatal(err)
	}
	got, _ := b.roleTargetsForTest()
	if got["aria-2"] != role || got["aria-1"] != "" {
		t.Fatalf("roles %v after recast, want only aria-2", got)
	}
	dup, _, err := b.CreateForm(role, form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": json.RawMessage(`"copy"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := b.RoleForms(); got[role] != "aria-2" || got[dup] != "aria-2" {
		t.Fatalf("roles %v after duplicating the role; both the role and its copy point at aria-2", got)
	}
	if _, err := b.ApplyForm(role, targetPatch("")); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(dup, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.roleTargetsForTest(); len(got) != 0 {
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
	if _, ready := b2.roleTargetsForTest(); ready {
		t.Fatal("a fresh backend claims a complete role index before reading any form")
	}
	b2.SeedRoles(context.Background(), 0)
	got, ready := b2.roleTargetsForTest()
	if !ready || got["aria-9"] != role {
		t.Fatalf("roles %v ready=%v after the seed, want aria-9 -> %s", got, ready, role)
	}
}

func namePatch(name string) form.Patch {
	if name == "" {
		return form.Build(form.Snapshot{}, nil, []string{nameKey})
	}
	raw, _ := json.Marshal(name)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{nameKey: raw}, nil)
}

// A name typed after @ is looked up on every resolve of an unknown ref; the
// index must follow renames, uncasts and deletes without opening a form, and
// rebuild itself after a restart.
func TestRolesByNameFollowEveryWrite(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	b.SeedRoles(context.Background(), 0)
	named := func(name string) []string {
		got, _, _ := b.FormsNamed(name)
		return got
	}
	role, _, err := b.CreateForm("", targetPatch("aria-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyForm(role, namePatch("figaro")); err != nil {
		t.Fatal(err)
	}
	if got := named("figaro"); len(got) != 1 || got[0] != role {
		t.Fatalf("roles named figaro %v, want [%s]", got, role)
	}
	if _, err := b.ApplyForm(role, namePatch("barber")); err != nil {
		t.Fatal(err)
	}
	if got := named("figaro"); len(got) != 0 {
		t.Fatalf("a renamed role still answers to its old name: %v", got)
	}
	if _, err := b.ApplyForm(role, targetPatch("")); err != nil {
		t.Fatal(err)
	}
	if got := named("barber"); len(got) != 0 {
		t.Fatalf("a form that is no longer a role answers to its name as a role: %v", got)
	}
	if _, forms, _ := b.FormsNamed("barber"); len(forms) != 1 || forms[0] != role {
		t.Fatalf("an uncast form named barber is not listed among forms: %v", forms)
	}
	if _, err := b.ApplyForm(role, targetPatch("aria-2")); err != nil {
		t.Fatal(err)
	}
	b.Close()

	b, err = NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.SeedRoles(context.Background(), 0)
	if got := named("barber"); len(got) != 1 || got[0] != role {
		t.Fatalf("after a restart, roles named barber %v, want [%s]", got, role)
	}
	if err := b.Remove(role, true); err != nil {
		t.Fatal(err)
	}
	if got := named("barber"); len(got) != 0 {
		t.Fatalf("a deleted role answers to its name: %v", got)
	}
}

// A form not yet cast is found by its name too, after the roles; an aria's own
// board carrying a name key is not a form anyone casts, so it never answers.
func TestFormsByNameAreUnboundFormsOnly(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.SeedRoles(context.Background(), 0)
	plain, _, err := b.CreateForm("", namePatch("almaviva"))
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := b.CreateOutfit("named", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	aria, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyForm(aria, namePatch("almaviva")); err != nil {
		t.Fatal(err)
	}
	roles, forms, ready := b.FormsNamed("almaviva")
	if !ready || len(roles) != 0 || len(forms) != 1 || forms[0] != plain {
		t.Fatalf("named almaviva: roles %v forms %v ready=%v, want forms [%s] alone", roles, forms, ready, plain)
	}
}
