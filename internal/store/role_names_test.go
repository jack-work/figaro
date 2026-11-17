package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// :ls draws a role's address on the figaro it points at (@name, or the short
// @id of a role with no name); the list reads that from the index, so the
// index must follow a cast, a rename and an uncast.
func TestRoleNamesByTargetFollowTheIndex(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.SeedRoles(context.Background(), 0)
	role, _, err := b.CreateForm("", targetPatch("aria-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.RoleNamesByTarget()["aria-1"]; len(got) != 1 || !strings.HasPrefix(role, got[0]) || len(got[0]) >= len(role) {
		t.Fatalf("an unnamed role %s is drawn as %v, want its short @id", role, got)
	}
	if _, err := b.ApplyForm(role, namePatch("reviewer")); err != nil {
		t.Fatal(err)
	}
	other, _, err := b.CreateForm("", targetPatch("aria-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyForm(other, namePatch("boss")); err != nil {
		t.Fatal(err)
	}
	twin, _, err := b.CreateForm("", targetPatch("aria-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyForm(twin, namePatch("boss")); err != nil {
		t.Fatal(err)
	}
	if got := b.RoleNamesByTarget()["aria-1"]; !slices.Equal(got, []string{"@boss", "@reviewer"}) {
		t.Fatalf("aria-1 holds %v, want [@boss @reviewer]", got)
	}
	if _, err := b.ApplyForm(twin, targetPatch("")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyForm(other, targetPatch("")); err != nil {
		t.Fatal(err)
	}
	if got := b.RoleNamesByTarget()["aria-1"]; !slices.Equal(got, []string{"@reviewer"}) {
		t.Fatalf("after uncasting boss aria-1 holds %v", got)
	}
}

// The list asks on every tick; before SeedRoles finishes it must answer from
// what the index holds, never wait for the seed.
func TestRoleNamesByTargetAnswersBeforeTheSeed(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, ready := b.RoleForms(); ready {
		t.Fatal("fixture: the index is seeded already")
	}
	if got := b.RoleNamesByTarget(); len(got) != 0 {
		t.Fatalf("an unseeded index names roles: %v", got)
	}
}
