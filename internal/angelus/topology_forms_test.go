package angelus_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func ownerMintsForm(t *testing.T, r grantRig, name string) string {
	t.Helper()
	v, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch(name)})
	if err != nil {
		t.Fatal(err)
	}
	return v.(rpc.FormCreateResponse).FormID
}

// A v0.65.0 regression: @topology moves a form as well as an aria, so the
// owner can seat one role under another, and "" puts it back on top.
func TestTheOwnerDrawsARoleUnderAnotherRole(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	roleA, roleB := ownerMintsForm(t, r, "role a"), ownerMintsForm(t, r, "role b")
	top := presentedUnder(t, r, roleA)

	if err := setTopology(t, r, asOwner(), map[string]string{roleA: roleB}); err != nil {
		t.Fatalf("the owner drawing @roleA under @roleB: %v", err)
	}
	if up := presentedUnder(t, r, roleA); up != roleB {
		t.Fatalf("@roleA is drawn under %q, want @roleB %s", up, roleB)
	}

	aria, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, r, asOwner(), map[string]string{aria.FigaroID: roleA}); err != nil {
		t.Fatalf("the owner drawing an aria under @roleA: %v", err)
	}
	if up := presentedUnder(t, r, aria.FigaroID); up != roleA {
		t.Fatalf("the aria is drawn under %q, want @roleA %s", up, roleA)
	}

	if err := setTopology(t, r, asOwner(), map[string]string{roleA: ""}); err == nil || !strings.Contains(err.Error(), "cannot be drawn at the top") {
		t.Fatalf("drawing @roleA, now stored under @roleB, at the top = %v, want refused: the drawn tree has no top for it", err)
	}
	if up := presentedUnder(t, r, roleA); up != roleB {
		t.Fatalf("a refused draw moved @roleA under %q", up)
	}

	roleC := ownerMintsForm(t, r, "role c")
	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:*")
	if err := setTopology(t, r, mallory(), map[string]string{roleC: roleB}); err != nil {
		t.Fatalf("a drawn-only move of @roleC under @roleB: %v", err)
	}
	if n, _ := r.backend.Node(roleC); n.Parent != top {
		t.Fatalf("a drawn-only move stored @roleC under %q", n.Parent)
	}
	if err := setTopology(t, r, mallory(), map[string]string{roleC: ""}); err != nil {
		t.Fatalf("drawing @roleC back at the top: %v", err)
	}
	if up := presentedUnder(t, r, roleC); up != top {
		t.Fatalf("@roleC drawn at the top sits under %q, want %q", up, top)
	}
}

// The kill floor covers a moved form as it covers an aria: set on both ends
// does not draw a form elsewhere.
func TestDrawingAFormNeedsKillOnIt(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	roleA, roleB := ownerMintsForm(t, r, "role a"), ownerMintsForm(t, r, "role b")
	was := presentedUnder(t, r, roleA)

	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:aria:"+roleB)
	if err := setTopology(t, r, mallory(), map[string]string{roleA: roleB}); err == nil || !strings.Contains(err.Error(), "kill on aria:"+roleA) {
		t.Fatalf("drawing @roleA with set but no kill on it = %v, want refused on kill", err)
	}
	if up := presentedUnder(t, r, roleA); up != was {
		t.Fatalf("a refused draw moved @roleA under %q", up)
	}

	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:aria:"+roleA)
	if err := setTopology(t, r, mallory(), map[string]string{roleA: roleB}); err != nil {
		t.Fatalf("drawing @roleA with kill on it: %v", err)
	}
	if up := presentedUnder(t, r, roleA); up != roleB {
		t.Fatalf("@roleA is drawn under %q, want @roleB %s", up, roleB)
	}
}

// A box draws the forms it minted, and no form it lacks kill on.
func TestABoxDrawsOnlyItsOwnForms(t *testing.T) {
	d := boxDoorRig(t, "true")
	if err := wire.Install(d.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	owners := ownerMintsForm(t, d.grantRig, "the owner's")
	was := presentedUnder(t, d.grantRig, owners)

	c := d.dial(t)
	var made rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}}, &made); err != nil {
		t.Fatal(err)
	}
	mine := boxMintsForm(t, c, "the box's")

	if err := boxArrange(t, c, map[string]string{mine: made.FigaroID}); err != nil {
		t.Fatalf("the box drawing its own form under its aria: %v", err)
	}
	if up := presentedUnder(t, d.grantRig, mine); up != made.FigaroID {
		t.Fatalf("the box's form is drawn under %q, want %s", up, made.FigaroID)
	}

	if err := boxArrange(t, c, map[string]string{owners: made.FigaroID}); err == nil {
		t.Fatal("the box drew a form it has no kill on")
	}
	if up := presentedUnder(t, d.grantRig, owners); up != was {
		t.Fatalf("a refused box draw moved the owner's form under %q", up)
	}
}
