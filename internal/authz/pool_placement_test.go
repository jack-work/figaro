package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/internal/hierarchy"
)

type placementAsked struct{ box, node, under string }

func provisionerGuard(t *testing.T, asked *[]placementAsked, answer error) TopologyGuard {
	t.Helper()
	p := poolPolicies(t, brokerGroupsPolicy, leaseA, leaseB)
	return TopologyGuard{
		Policies: func() *Policies { return p },
		Owner:    func() jkauthz.Principal { return me },
		MintPlaces: func(box, node, under string) error {
			*asked = append(*asked, placementAsked{box, node, under})
			return answer
		},
	}
}

func placing(s jkauthz.Subject) hierarchy.Principal {
	return hierarchy.Principal{Name: s.Principal.String(), Subject: TopologyCaller{Subject: s, Door: DoorInProcess}}
}

func placeOf(node, under string) hierarchy.Plan {
	return hierarchy.Plan{Op: hierarchy.Place, Node: node, To: under, Moves: []hierarchy.Move{{Node: node, To: under, Tree: hierarchy.Storage}}}
}

// Ruling (d). The provisioner holds kill on its lease's figaros, and kill is
// what a move in the drawn tree asks of anyone else. For the provisioner
// the guard asks one thing instead: is this the placement its mint is
// making. Every other move is refused before any grant is read, and so is
// a placement by a provisioner subject that serves no lease or two. A
// restore is refused with them: kill on the revived node is all it asks,
// and a lease handler has no business reviving what a release reaped.
func TestTheProvisionerMovesANodeOnlyByThePlacementItsMintMakes(t *testing.T) {
	serving := ProvisionerServing(leaseA)
	var asked []placementAsked
	g := provisionerGuard(t, &asked, nil)

	if _, err := g.Check(context.Background(), placing(serving), placeOf("in-a", "pool-node")); err != nil {
		t.Fatalf("the placement the mint vouches for was refused: %v", err)
	}
	if want := []placementAsked{{leaseA, "in-a", "pool-node"}}; !slices.Equal(asked, want) {
		t.Fatalf("the guard asked the mint %v, want %v", asked, want)
	}

	asked = nil
	drawn := []hierarchy.Move{{Node: "in-a", From: "x", To: "in-b", Tree: hierarchy.Drawn}}
	for name, plan := range map[string]hierarchy.Plan{
		"an arrange of its own lease's figaro": {Op: hierarchy.Arrange, Node: "in-a", Moves: drawn},
		"a promote of its own lease's figaro":  {Op: hierarchy.Promote, Node: "in-a", Moves: drawn},
		"a demote of its own lease's figaro":   {Op: hierarchy.Demote, Node: "in-a", To: "in-b", Moves: drawn},
		"a restore of its own lease's figaro":  {Op: hierarchy.Restore, Revive: []string{"in-a"}},
		"a placement that moves a second node": {Op: hierarchy.Place, Node: "in-a", To: "pool-node", Moves: []hierarchy.Move{{Node: "in-a", To: "pool-node", Tree: hierarchy.Storage}, {Node: "in-b", To: "pool-node", Tree: hierarchy.Storage}}},
		"a placement that lands elsewhere":     {Op: hierarchy.Place, Node: "in-a", To: "pool-node", Moves: []hierarchy.Move{{Node: "in-a", To: "owners", Tree: hierarchy.Storage}}},
	} {
		if _, err := g.Check(context.Background(), placing(serving), plan); err == nil || errors.Is(err, hierarchy.ErrDrawnOnly) {
			t.Errorf("%s was not refused outright: %v", name, err)
		}
	}
	for name, s := range map[string]jkauthz.Subject{
		"no lease":   {Principal: ProvisionerPrincipal()},
		"two leases": {Principal: ProvisionerPrincipal(), Groups: []string{leaseA + "@" + PoolScope, leaseB + "@" + PoolScope}},
	} {
		if _, err := g.Check(context.Background(), placing(s), placeOf("in-a", "pool-node")); err == nil {
			t.Errorf("a provisioner serving %s placed a node", name)
		}
	}
	if len(asked) != 0 {
		t.Errorf("a refusal the plan's shape decides still asked the mint: %v", asked)
	}

	refusing := provisionerGuard(t, &asked, errors.New("not minted in this call"))
	if _, err := refusing.Check(context.Background(), placing(serving), placeOf("in-a", "pool-node")); err == nil || !strings.Contains(err.Error(), "not minted in this call") {
		t.Errorf("a placement the mint does not vouch for: %v", err)
	}
	unwired := provisionerGuard(t, &asked, nil)
	unwired.MintPlaces = nil
	if _, err := unwired.Check(context.Background(), placing(serving), placeOf("in-a", "pool-node")); err == nil {
		t.Error("with no mint to ask, the provisioner placed a node")
	}
}
