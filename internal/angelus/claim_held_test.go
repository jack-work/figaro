package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// El Inspector XV: a claim for a figaro the store does not hold ran with no
// figaro ceiling, so a killed scoped child's lingering agent kept the owner's
// authority on its in-process calls. Such a claim is now refused there. At
// a door the claim is the caller's assertion and an unheld one names a
// figaro elsewhere (a forwarding node's, or FIGARO_ARIA from another
// daemon's shell), so it is dropped and the caller's own authority decides.
func TestACallClaimingAFigaroNotHeldIsRefused(t *testing.T) {
	r, p, x, _ := guardChain(t)
	narrow(t, r, x, "read:*")
	target, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	t1 := target.FigaroID
	if _, err := killAs(r, rpc.KillRequest{FigaroID: p, Recursive: true, Now: true}); err != nil {
		t.Fatal(err)
	}
	if !absent(r, x) {
		t.Fatal("control: kill -r did not take X")
	}

	onT1 := []struct {
		name   string
		method string
		req    any
	}{
		{"read", rpc.MethodRead, rpc.ReadRequest{FigaroID: t1}},
		{"set", rpc.MethodSet, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:*"})}},
		{"kill", rpc.MethodKill, rpc.KillRequest{FigaroID: t1, Now: true}},
	}
	create, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd})
	doors := []struct {
		name string
		ctx  context.Context
	}{
		{"in-process", authz.WithDoor(asOwner(), authz.DoorInProcess)},
		{"no door", asOwner()},
	}
	for _, claim := range []string{x, "0123456789abcdef0123456789abcdef"} {
		for _, door := range doors {
			for _, c := range onT1 {
				if _, err := r.h[c.method](door.ctx, claimed(t, claim, withAria(t, t1, c.req))); err == nil {
					t.Errorf("%s claiming %s: %s on the owner's aria ran", door.name, claim[:8], c.name)
				}
			}
			_, err := r.h[rpc.MethodCreate](door.ctx, claimed(t, claim, create))
			if err == nil || !strings.Contains(err.Error(), "figaro:"+claim+" is not held here") {
				t.Errorf("%s claiming %s: create = %v, want refused as not held", door.name, claim[:8], err)
			}
		}
	}
	if absent(r, t1) {
		t.Fatal("a refused kill took the owner's aria")
	}
	if got := scopesOf(t, r, t1); got != "" {
		t.Fatalf("a refused set narrowed the owner's aria to %s", got)
	}

	for _, d := range []authz.Door{authz.DoorLocal, authz.DoorNode} {
		if _, err := r.h[rpc.MethodRead](authz.WithDoor(asOwner(), d), claimed(t, x, withAria(t, t1, rpc.ReadRequest{FigaroID: t1}))); err != nil {
			t.Errorf("%s door: a claim not held here is dropped and the caller's own authority reads: %v", d, err)
		}
	}
	for _, c := range onT1 {
		if _, err := r.h[c.method](asOwner(), withAria(t, t1, c.req)); err != nil {
			t.Errorf("the owner with no claim, %s: %v", c.name, err)
		}
	}
}
