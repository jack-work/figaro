package authz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/hierarchy"
)

// K6: the fresh-placement pass needs the target inside the fork's lineage;
// outside it, a box's placement is held to rule 2 and only drawn.
func TestAPlaceOutsideTheForksLineageIsDrawnOnly(t *testing.T) {
	g := topologyGuard(t, `
[roles.box]
grants = ["*:*"]

[[bind]]
role = "box"
principals = ["sandbox:judge"]
`)
	box := guardCaller(jkauthz.KindSandbox, "judge", authn.DoorBox)
	move := hierarchy.Move{Node: "alt", From: "trunk", To: "elsewhere", Tree: hierarchy.Storage, NodeOwner: box.Name}
	_, err := g.Check(context.Background(), box, hierarchy.Plan{Op: hierarchy.Place, Node: "alt", Moves: []hierarchy.Move{move}})
	if !errors.Is(err, hierarchy.ErrDrawnOnly) {
		t.Fatalf("a box placing its fork outside the lineage = %v, want ErrDrawnOnly", err)
	}
	move.WithinLineage = true
	if _, err := g.Check(context.Background(), box, hierarchy.Plan{Op: hierarchy.Place, Node: "alt", Moves: []hierarchy.Move{move}}); err != nil {
		t.Fatalf("a box placing its fresh fork inside the lineage: %v", err)
	}
}

// K7: the box door is never the owner, even if its principal were to equal him.
func TestTheBoxDoorIsNeverTheOwner(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	onBox := guardCaller(guardOwner.Kind, guardOwner.ID, authn.DoorBox)
	move := hierarchy.Move{Node: "a", From: "s", To: "b", Tree: hierarchy.Storage, NodeOwner: guardOwner.String()}
	_, err := g.Check(context.Background(), onBox, hierarchy.Plan{Op: hierarchy.Arrange, Moves: []hierarchy.Move{move}})
	if !errors.Is(err, hierarchy.ErrDrawnOnly) {
		t.Fatalf("the owner's principal on the box door moving storage = %v, want ErrDrawnOnly", err)
	}
}

// P4: a tool shell on the local door reaches the topology guard as its
// figaro, judged by that figaro's lineage and grants, never as the owner.
func TestAToolShellsTopologyOpIsJudgedAsItsFigaro(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	move := hierarchy.Move{Node: "a", From: "s", To: "b", Tree: hierarchy.Storage, NodeOwner: guardOwner.String()}
	plan := hierarchy.Plan{Op: hierarchy.Arrange, Moves: []hierarchy.Move{move}}
	owner := authn.WithDoor(authn.WithSubject(context.Background(), jkauthz.Subject{Principal: guardOwner}), authn.DoorLocal)
	asOwner, err := TopologyPrincipal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Check(context.Background(), asOwner, plan); err != nil {
		t.Fatalf("the owner at a terminal moving storage: %v", err)
	}
	asTool, err := TopologyPrincipal(authn.WithToolOf(owner, "f00d"))
	if err != nil {
		t.Fatal(err)
	}
	if c := asTool.Subject.(TopologyCaller); c.Figaro != "f00d" {
		t.Fatalf("the tool shell reached the guard as figaro %q", c.Figaro)
	}
	if _, err := g.Check(context.Background(), asTool, plan); err == nil || !strings.Contains(err.Error(), "f00d") {
		t.Fatalf("a tool shell moving the owner's storage = %v, want a refusal judged as figaro f00d", err)
	}
}
