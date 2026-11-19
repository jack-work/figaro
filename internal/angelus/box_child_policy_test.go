package angelus_test

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// A box door that names no figaro holds no figaro grants, so it writes no
// child policy by any route, whichever way protected keys are kept.
func TestAnUnclaimedBoxWritesNoChildPolicy(t *testing.T) {
	for mode, extra := range map[string]string{"lineage": "", "open": protectedOpen} {
		d := boxDoorRigConfigured(t, defaultPolicy, "true", extra)
		inBox, err := d.create(asOwner(), "judged")
		if err != nil {
			t.Fatal(err)
		}
		c := d.dial(t)
		p := *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{form.ScopesKey: []string{"*:*"}}})
		for route, call := range map[string]func() error{
			"set": func() error {
				return boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: p}, nil)
			},
			"fork": func() error {
				return c.Call(context.Background(), rpc.MethodFork, mustJSON(t, rpc.ForkRequest{FigaroID: inBox.FigaroID, Patch: &p}), nil)
			},
			"create": func() error {
				return c.Call(context.Background(), rpc.MethodCreate, mustJSON(t, rpc.CreateRequest{Patch: &p}), nil)
			},
			"cast": func() error {
				return boxOnAria(t, c, rpc.MethodCast, inBox.FigaroID, rpc.CastRequest{RolePatch: &p}, nil)
			},
		} {
			if err := call(); err == nil {
				t.Errorf("%s: an unclaimed box wrote a child policy by %s", mode, route)
			}
		}
		if _, ok := keyOn(t, d.grantRig, inBox.FigaroID, form.ChildPolicyKey); ok {
			t.Fatalf("%s: a child policy landed on the box's aria", mode)
		}
	}
}
