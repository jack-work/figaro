package angelus_test

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
)

const unboxedCeilingPolicy = `
[roles.owner]
grants = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[roles.box]
grants = [
  "create:*",
  "use:sandbox:self",
  "read:sandboxed:self", "listen:sandboxed:self", "send:sandboxed:self", "fork:sandboxed:self", "set:sandboxed:self", "kill:sandboxed:self",
]

[[bind]]
role = "box"
principals = ["sandbox:judge@local:box"]

[roles.lent]
grants = ["read:unboxed", "listen:unboxed", "send:unboxed", "fork:unboxed", "set:unboxed"]

[[bind]]
role = "lent"
principals = ["troupe:kelliher"]
`

// The troupe reaches a node only through the ceiling the node lends it, and
// a ceiling of unboxed stops at the box: troupe:kelliher cannot set an aria
// in a box at all, so it cannot set its system.sandbox either, while an
// aria outside every box takes the same write.
func TestTheTroupeCannotSetTheSandboxOfABoxAria(t *testing.T) {
	d := boxDoorRigWith(t, unboxedCeilingPolicy, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	troupe := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalKind(authz.KindTroupe), ID: "kelliher"})
	for _, to := range []string{"", "local"} {
		_, err := d.h[rpc.MethodSet](troupe, withAria(t, inBox.FigaroID, rpc.SetRequest{Patch: *sandboxPatch(to)}))
		if err == nil {
			t.Errorf("troupe:kelliher set system.sandbox %q on an aria in a box", to)
		}
	}
	if got := d.sandboxOf(t, inBox.FigaroID); got != "judge" {
		t.Fatalf("a refused write landed: sandbox %q", got)
	}
	if _, err := d.h[rpc.MethodSet](troupe, withAria(t, outside.FigaroID, rpc.SetRequest{Patch: *mantraPatch("reached")})); err != nil {
		t.Fatalf("troupe:kelliher on an unboxed aria: %v", err)
	}
	if _, err := d.h[rpc.MethodSet](troupe, withAria(t, inBox.FigaroID, rpc.SetRequest{Patch: *mantraPatch("reached")})); err == nil {
		t.Fatal("troupe:kelliher set an aria in a box")
	}
}

// The store's fact fails closed: a form the store cannot read is in no
// sandbox the selectors will name, so unboxed does not match it.
func TestTheStoreSaysNothingAboutAFormItCannotRead(t *testing.T) {
	r := newGrantRig(t)
	made, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	facts := angelus.StoreFacts(r.backend)
	if sb, ok := facts.SandboxOf(made.FigaroID); !ok || sb != "" {
		t.Fatalf("a local aria: %q, %v", sb, ok)
	}
	if sb, ok := facts.SandboxOf("ffffffffffffffffffffffffffffffff"); ok {
		t.Fatalf("an aria the store does not hold read as %q", sb)
	}
}
