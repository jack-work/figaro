package store

import (
	"context"
	"slices"
	"testing"
)

// :ls draws a role's name on the figaro it points at; the list reads that from
// the index, so the index must follow a cast, a rename and an uncast.
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
	if got := b.RoleNamesByTarget(); len(got) != 0 {
		t.Fatalf("an unnamed role is drawn as %v", got)
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
	if got := b.RoleNamesByTarget()["aria-1"]; !slices.Equal(got, []string{"boss", "reviewer"}) {
		t.Fatalf("aria-1 holds %v, want [boss reviewer]", got)
	}
	if _, err := b.ApplyForm(other, targetPatch("")); err != nil {
		t.Fatal(err)
	}
	if got := b.RoleNamesByTarget()["aria-1"]; !slices.Equal(got, []string{"reviewer"}) {
		t.Fatalf("after uncasting boss aria-1 holds %v", got)
	}
}
