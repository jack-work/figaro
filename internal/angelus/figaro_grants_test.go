package angelus_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func grantsOf(t *testing.T, r grantRig, id string) rpc.GrantsResponse {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodGrants, rpc.GrantsRequest{FigaroID: id})
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.GrantsResponse)
}

func TestNoGrantsAnywhereServesEveryFigaroVerbAndNoForeignOne(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "")
	top, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: top.FigaroID})
	if err != nil {
		t.Fatal(err)
	}
	kid := out.(rpc.ForkResponse).Alternative
	g := grantsOf(t, r, kid)
	if strings.Join(g.Effective, ",") != "*:*" {
		t.Fatalf("effective %v", g.Effective)
	}
	for _, e := range g.Effective {
		if v, _, _ := strings.Cut(e, ":"); v != "*" && v != "read" {
			t.Fatalf("a figaro with no grants carries %s", e)
		}
	}
	if g.CeilingSet || len(g.Chain) != 2 || g.Chain[0].FigaroID != top.FigaroID || g.Chain[1].Set {
		t.Fatalf("%+v", g)
	}
}

func TestAFigarosGrantsNarrowItsCallsThroughTheBoxDoor(t *testing.T) {
	r := newLineageRig(t, "")
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:*", "set:aria:self"})})); err != nil {
		t.Fatal(err)
	}
	c := r.dial(t)
	note := *heldPatch(t, map[string]any{"note": "hi"})
	if err := r.setAs(t, c, r.child, r.child, note); err != nil {
		t.Fatalf("set:aria:self: %v", err)
	}
	err := r.setAs(t, c, r.child, r.sibling, note)
	if err == nil || !strings.Contains(err.Error(), "figaro:"+r.child) {
		t.Fatalf("the child set its sibling: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.sibling, note); err != nil {
		t.Fatalf("the parent holds *:*: %v", err)
	}
	if g := grantsOf(t, r.grantRig, r.child); strings.Join(g.Effective, ",") != "read:*,set:aria:"+r.child {
		t.Fatalf("effective %v", g.Effective)
	}
}

func TestPromoteKeepsTheNarrowingAFigaroHadUnderItsParent(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "")
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	top, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, top.FigaroID, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:*", "egress:a:1"})})); err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: top.FigaroID})
	if err != nil {
		t.Fatal(err)
	}
	kid := out.(rpc.ForkResponse).Alternative
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, kid, rpc.SetRequest{Patch: grantsPatch(t, []string{"*:*"})})); err != nil {
		t.Fatal(err)
	}
	before := grantsOf(t, r, kid).Effective
	res, err := r.call(asOwner(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: kid, Levels: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.(rpc.PromoteResponse).Climbed == 0 {
		t.Fatalf("promote did not climb: %+v", res)
	}
	after := grantsOf(t, r, kid)
	t.Logf("chain after %+v, own %v", after.Chain, after.Chain[len(after.Chain)-1].Grants)
	if strings.Join(after.Effective, ",") != strings.Join(before, ",") {
		t.Fatalf("promote widened %v to %v", before, after.Effective)
	}
}

func TestTheBoxDoorRefusesAnUnclaimedCallWhenConfigured(t *testing.T) {
	r := newLineageRig(t, "[authz]\nfigaro_claim = \"required\"\n")
	c := r.dial(t)
	var out rpc.FormResponse
	if err := c.Call(context.Background(), rpc.MethodForm, withAria(t, r.child, rpc.FormRequest{}), &out); err == nil || !strings.Contains(err.Error(), "figaro_claim") {
		t.Fatalf("an unclaimed box call passed: %v", err)
	}
	if err := c.Call(context.Background(), rpc.MethodForm, claimed(t, r.parent, withAria(t, r.child, rpc.FormRequest{})), &out); err != nil {
		t.Fatalf("a claimed call: %v", err)
	}
}
