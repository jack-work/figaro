package authz

import (
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"
)

const everyBoxIsOwner = `
[roles.all]
permissions = ["*:*"]

[[bind]]
role = "all"
principals = ["sandbox:*"]
`

// On a pool daemon a box named like a lease is judged by the lease's
// computed grant alone, say on its own lease while the lease is in force,
// whatever the policy file binds; it holds no role for the methods that
// have no verb; and a name no lease holds holds nothing.
func TestALeasesPrincipalHoldsSayOnItsOwnLeaseAndNothingElse(t *testing.T) {
	p := poolPolicies(t, everyBoxIsOwner, leaseA)
	p.SetPoolDaemon(true)
	a := jkauthz.Subject{Principal: BoxPrincipal(leaseA)}
	if got, want := heldBy(p, a), []string{"say on " + Lease(leaseA).String()}; !slices.Equal(got, want) {
		t.Errorf("a live lease's principal holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p.HoldsRole(a) {
		t.Error("a lease's principal holds a role the policy file binds")
	}
	if held := heldBy(p, jkauthz.Subject{Principal: BoxPrincipal(leaseB)}); len(held) != 0 {
		t.Errorf("a lease name that is not in force holds %v", held)
	}
	p.SetPoolDaemon(false)
	if !p.HoldsRole(a) || len(heldBy(p, a)) < 2 {
		t.Error("on a daemon with no pool the same binding gives the box nothing, so this test would pass for the wrong reason")
	}
}
