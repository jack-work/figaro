package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
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
	short := b.RoleNamesByTarget()["aria-1"]
	if len(short) != 1 || !strings.HasPrefix(role, short[0]) || len(short[0]) != 1+rpc.ShortIDLen {
		t.Fatalf("an unnamed role %s is drawn as %v, want its short @id", role, short)
	}
	for range 3 {
		if _, _, err := b.CreateForm("", targetPatch("aria-2")); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.RoleNamesByTarget()["aria-1"]; !slices.Equal(got, short) {
		t.Fatalf("other roles arriving moved the unnamed role's address from %v to %v", short, got)
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

// An unnamed role's short @id is something to type: when another figaro or
// form shares its prefix the address is the full id, never an ambiguous one.
func TestUnnamedRoleAddressFallsBackToTheFullIDOnACollision(t *testing.T) {
	const role = "@abcde0123456789abcdef0123456789a"
	ids := map[string][]string{}
	match := func(prefix string, limit int) []string {
		out := ids[prefix]
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	ids["@abcde"] = []string{role}
	if got := unnamedRoleAddress(role, match); got != "@abcde" {
		t.Fatalf("alone, the role is drawn as %s", got)
	}
	ids["@abcde"] = []string{role, "@abcde999999999999999999999999999"}
	if got := unnamedRoleAddress(role, match); got != role {
		t.Fatalf("beside a form sharing its prefix, the role is drawn as %s", got)
	}
	ids["@abcde"] = []string{role}
	ids["abcde"] = []string{"abcde777777777777777777777777777"}
	if got := unnamedRoleAddress(role, match); got != role {
		t.Fatalf("beside a figaro sharing its prefix, the role is drawn as %s", got)
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
