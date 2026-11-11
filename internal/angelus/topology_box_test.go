package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	jkrpc "github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func boxArrange(t *testing.T, c *jkrpc.Client, set map[string]string, unset ...string) error {
	t.Helper()
	keys := map[string]json.RawMessage{}
	for k, v := range set {
		keys[k], _ = json.Marshal(v)
	}
	req := rpc.SetRequest{Patch: form.Build(form.Snapshot{}, keys, unset), Assert: len(unset) > 0}
	return boxOnAria(t, c, rpc.MethodSet, rpc.TopologyFormID, req, nil)
}

// A box arranges @topology through its own door with the box's grants: it
// moves only arias in the box, under arias in the box, and an id outside the
// box that resolves through knownID is answered exactly as an id that names
// no aria. Its @topology holds no key for an aria outside it.
func TestABoxArrangesOnlyItsOwnArias(t *testing.T) {
	d := boxDoorRig(t, "true")
	if err := wire.Install(d.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	another, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, d.grantRig, asOwner(), map[string]string{outside.FigaroID: another.FigaroID}); err != nil {
		t.Fatal(err)
	}

	c := d.dial(t)
	var made rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}}, &made); err != nil {
		t.Fatal(err)
	}
	var forked rpc.ForkResponse
	if err := c.Call(context.Background(), rpc.MethodFork, rpc.ForkRequest{FigaroID: made.FigaroID}, &forked); err != nil {
		t.Fatal(err)
	}
	kid := forked.Alternative

	if err := boxArrange(t, c, map[string]string{kid: inBox.FigaroID}); err != nil {
		t.Fatalf("the box drawing its aria under another in the box: %v", err)
	}
	if up := presentedUnder(t, d.grantRig, kid); up != inBox.FigaroID {
		t.Fatalf("after the box's set %s is presented under %q, want %s", kid, up, inBox.FigaroID)
	}

	var view rpc.FormResponse
	if err := boxOnAria(t, c, rpc.MethodForm, rpc.TopologyFormID, rpc.FormRequest{}, &view); err != nil {
		t.Fatalf("the box reading @topology: %v", err)
	}
	if got := view.Snapshot.Lookup(kid); got == nil || *got != inBox.FigaroID {
		t.Errorf("the box's @topology %s = %v, want %s", kid, got, inBox.FigaroID)
	}
	for k := range view.Snapshot.All() {
		if k == outside.FigaroID || k == another.FigaroID {
			t.Errorf("the box's @topology names %s, an aria outside the box", k)
		}
	}

	const nowhere = "0123456789abcdef0123456789abcdef"
	moved := boxArrange(t, c, map[string]string{outside.FigaroID: inBox.FigaroID})
	if moved == nil {
		t.Fatal("the box moved an aria outside it")
	}
	if ghost := boxArrange(t, c, map[string]string{nowhere: inBox.FigaroID}); ghost == nil ||
		strings.ReplaceAll(moved.Error(), outside.FigaroID, "X") != strings.ReplaceAll(ghost.Error(), nowhere, "X") {
		t.Errorf("moving an aria outside the box is told apart from moving none: %q vs %q", moved, ghost)
	}
	if up := presentedUnder(t, d.grantRig, outside.FigaroID); up != another.FigaroID {
		t.Errorf("a refused box write moved %s under %q", outside.FigaroID, up)
	}
	if err := boxArrange(t, c, nil, outside.FigaroID); err == nil {
		t.Error("the box cleared an override on an aria outside it")
	}
	if up := presentedUnder(t, d.grantRig, outside.FigaroID); up != another.FigaroID {
		t.Errorf("a refused box clear moved %s under %q", outside.FigaroID, up)
	}

	under := boxArrange(t, c, map[string]string{kid: outside.FigaroID})
	if under == nil {
		t.Fatal("the box drew its aria under one outside the box")
	}
	none := boxArrange(t, c, map[string]string{kid: nowhere})
	if none == nil {
		t.Fatal("the box drew its aria under an aria that does not exist")
	}
	if strings.ReplaceAll(under.Error(), outside.FigaroID, "X") != strings.ReplaceAll(none.Error(), nowhere, "X") {
		t.Errorf("an aria outside the box is told apart from none: %q vs %q", under, none)
	}

	if err := boxArrange(t, c, map[string]string{inBox.FigaroID: kid}); err == nil {
		t.Error("the box drew an aria under one drawn under it")
	}
	if err := boxArrange(t, c, nil, kid); err != nil {
		t.Fatalf("the box clearing its own override: %v", err)
	}
	if up := presentedUnder(t, d.grantRig, kid); up != made.FigaroID {
		t.Errorf("after the box's clear %s is presented under %q, want its history parent %s", kid, up, made.FigaroID)
	}
}

// El Inspector S1: a storage move re-parents the grant chain and the
// protected-key ancestors. The owner's move re-parents A; the box's move of
// the same A under its own figaro F is drawn only, so A keeps its history
// parent and its narrowed effective grants, and F does not become its
// ancestor.
func TestABoxMoveIsDrawnOnlyAndLeavesGrantsAlone(t *testing.T) {
	d := boxDoorRig(t, "true")
	if err := wire.Install(d.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	mint := func() string {
		t.Helper()
		resp, err := d.create(asOwner(), "judged")
		if err != nil {
			t.Fatal(err)
		}
		return resp.FigaroID
	}
	o, a, f := mint(), mint(), mint()
	narrowed, _ := json.Marshal([]string{"read:sandboxed:self"})
	if _, err := d.backend.ApplyForm(o, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.GrantsKey: narrowed}, nil)); err != nil {
		t.Fatal(err)
	}

	if err := setTopology(t, d.grantRig, asOwner(), map[string]string{a: o}); err != nil {
		t.Fatalf("the owner moving A under O: %v", err)
	}
	if n, _ := d.backend.Node(a); n.Parent != o {
		t.Fatalf("after the owner's move A's history parent is %q, want O %s", n.Parent, o)
	}
	before, held := d.a.Policies.Effective(a)
	if !held {
		t.Fatal("A has no effective grants to compare")
	}
	if wide, _ := d.a.Policies.Effective(f); fmt.Sprint(wide) == fmt.Sprint(before) {
		t.Fatalf("setup: F's effective grants %v are no wider than A's", wide)
	}

	if err := boxArrange(t, d.dial(t), map[string]string{a: f}); err != nil {
		t.Fatalf("the box arranging A under F: %v", err)
	}
	if n, _ := d.backend.Node(a); n.Parent != o {
		t.Fatalf("the box's move re-parented A's history under %q, want it still under O %s", n.Parent, o)
	}
	if up := presentedUnder(t, d.grantRig, a); up != f {
		t.Fatalf("the box's move presents A under %q, want F %s", up, f)
	}
	if after, _ := d.a.Policies.Effective(a); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("A's effective grants moved from %v to %v", before, after)
	}
	chain, _ := angelus.StoreFacts(d.backend).(authz.FigaroFacts).Ancestry(a)
	if slices.Contains(chain, f) {
		t.Fatalf("F became an ancestor of A, so it may write A's system.grants: %v", chain)
	}
}
