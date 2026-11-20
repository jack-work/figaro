package authz

import (
	"testing"

	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/hierarchy"
)

// A figaro on the local door carries the owner's principal; it must not
// count as the owner when the guard decides consent.

func labeled(figaro string) hierarchy.Principal {
	by := guardCaller(guardOwner.Kind, guardOwner.ID, authn.DoorLocal)
	c := by.Subject.(TopologyCaller)
	c.Figaro = figaro
	by.Subject = c
	return by
}

const narrowFigaroPolicy = `
[roles.owner]
grants = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:1000"]
`

func TestAFigaroOnTheLocalDoorIsNotTheOwner(t *testing.T) {
	g := topologyGuard(t, narrowFigaroPolicy)
	if !g.isOwner(guardCaller(guardOwner.Kind, guardOwner.ID, authn.DoorLocal).Subject.(TopologyCaller)) {
		t.Fatal("control: the unlabeled owner is the owner")
	}
	if g.isOwner(labeled("a").Subject.(TopologyCaller)) {
		t.Fatal("a figaro on the local door counted as the owner")
	}
}
