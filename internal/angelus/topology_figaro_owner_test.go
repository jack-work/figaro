package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

// Review of H1 (F2): a figaro on the local door carries the owner's
// principal, and the topology guard took it for the owner. It could shed a
// child under a figaro outside its lineage to get under its own
// max_children, and draw itself under a role's holder to cast itself into
// that role, both without the receiving parent's consent.

type ownerRig struct {
	grantRig
	self, kid, q, holder, role string
}

func newOwnerRig(t *testing.T, scopes func(ownerRig) []string) ownerRig {
	t.Helper()
	r := ownerRig{grantRig: newRigWith(t, doorPolicy)}
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	mint := func() string {
		out, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		return out.FigaroID
	}
	r.self, r.kid, r.q, r.holder = mint(), mint(), mint(), mint()
	if scopes != nil {
		raw, _ := json.Marshal(scopes(r))
		if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.self, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: raw}, nil)})); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.arrange(t, "", r.kid, r.self); err != nil {
		t.Fatalf("the owner drawing the child under its figaro: %v", err)
	}
	holder, _ := json.Marshal(r.holder)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"seat"`), "target-aria": holder}, nil)
	out, err := r.call(localOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: &patch})
	if err != nil {
		t.Fatal(err)
	}
	r.role = out.(rpc.FormCreateResponse).FormID
	return r
}

func (r ownerRig) arrange(t *testing.T, figaro, node, under string) error {
	t.Helper()
	to, _ := json.Marshal(under)
	raw := withAria(t, rpc.TopologyFormID, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{node: to}, nil)})
	if figaro != "" {
		raw = claimed(t, figaro, raw)
	}
	_, err := r.h[rpc.MethodSet](localOwner(), raw)
	return err
}

func (r ownerRig) drawnUnder(t *testing.T, node string) string {
	t.Helper()
	parent, _ := r.a.Topology.Drawn(node)
	return parent
}

func (r ownerRig) refusesShedAndHijack(t *testing.T) {
	t.Helper()
	if err := r.arrange(t, r.self, r.kid, r.q); err == nil || !strings.Contains(err.Error(), "waits on") {
		t.Errorf("shedding the child under %s: %v, want it to wait on that parent", r.q, err)
	}
	if got := r.drawnUnder(t, r.kid); got != r.self {
		t.Errorf("a figaro shed its child under %s, outside its lineage, without consent: the child is drawn under %q", r.q, got)
	}
	if err := r.arrange(t, r.self, r.self, r.holder); err == nil || !strings.Contains(err.Error(), "waits on") {
		t.Errorf("drawing itself under %s: %v, want it to wait on that holder", r.holder, err)
	}
	if got := r.drawnUnder(t, r.self); got == r.holder {
		t.Errorf("a figaro drew itself under the role holder %s without consent", r.holder)
	}
	if _, err := r.h[rpc.MethodCast](localOwner(), claimed(t, r.self, withAria(t, r.self, rpc.CastRequest{FormID: r.role}))); err == nil {
		t.Errorf("a figaro cast itself into %s, a role held by %s outside its lineage", r.role, r.holder)
	}
}

func TestAFigaroWithNarrowScopesCannotShedAChildOrTakeAHeldRole(t *testing.T) {
	r := newOwnerRig(t, func(r ownerRig) []string {
		return []string{"*:aria:self", "*:aria:" + r.kid, "read:*"}
	})
	r.refusesShedAndHijack(t)
}

// Review of F2: an in-place swap moves a node up into a parent that already
// holds it, so it waits on no one; demote and succeed must not park.
func TestAScopedSeatDemotesItselfUnderItsForkWithoutConsent(t *testing.T) {
	r := ownerRig{grantRig: newRigWith(t, doorPolicy)}
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	out, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	engineer := out.FigaroID
	fork := func(of string) string {
		raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: of, Patch: heldPatch(t, map[string]any{form.FanoutMaxChildrenKey: 2})})
		out, err := r.h[rpc.MethodFork](asOwner(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return out.(rpc.ForkResponse).Alternative
	}
	seat := fork(engineer)
	heir := fork(seat)
	scopes, _ := json.Marshal([]string{"*:aria:self", "*:aria:" + heir, "read:*"})
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, seat, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: scopes}, nil)})); err != nil {
		t.Fatal(err)
	}
	if got := r.drawnUnder(t, seat); got != engineer {
		t.Fatalf("the seat is drawn under %q, want %q", got, engineer)
	}
	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: seat, Prefer: heir})
	res, err := r.h[rpc.MethodDemote](localOwner(), claimed(t, seat, raw))
	if err != nil {
		t.Fatalf("a scoped seat demoting itself under its fork: %v", err)
	}
	if dem := res.(rpc.DemoteResponse); dem.Parked != nil {
		t.Fatalf("the demote parked on %s, which already held the fork", dem.Parked.Parent)
	}
	if got := r.drawnUnder(t, heir); got != engineer {
		t.Errorf("the fork is drawn under %q after the demote, want %q", got, engineer)
	}
	if got := r.drawnUnder(t, seat); got != heir {
		t.Errorf("the seat is drawn under %q after its demote, want %q", got, heir)
	}
}

// Review of F2: a claim the daemon drops (a figaro killed, or never held
// here) must not leave its caller reading as the owner.
func TestADroppedClaimIsNotTheOwner(t *testing.T) {
	r := newOwnerRig(t, func(r ownerRig) []string { return []string{"read:*"} })
	const unheld = "0123456789abcdef0123456789abcdef"
	_ = r.arrange(t, unheld, r.kid, r.q)
	if got := r.drawnUnder(t, r.kid); got != r.self {
		t.Errorf("a dropped claim moved %s under %q without consent", r.kid, got)
	}
	if _, err := r.h[rpc.MethodCast](localOwner(), claimed(t, unheld, withAria(t, r.q, rpc.CastRequest{FormID: r.role}))); err == nil {
		t.Errorf("a dropped claim cast %s into %s, a role held by %s", r.q, r.role, r.holder)
	}
	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: r.self})
	if out, err := r.h[rpc.MethodDemote](localOwner(), claimed(t, unheld, raw)); err == nil && out.(rpc.DemoteResponse).Parked == nil {
		if got := r.drawnUnder(t, r.self); got == r.kid {
			t.Errorf("a dropped claim demoted %s under %s without consent", r.self, r.kid)
		}
	}
	if err := r.arrange(t, "", r.kid, r.q); err != nil || r.drawnUnder(t, r.kid) != r.q {
		t.Fatalf("control: the owner at the terminal: %v, under %q", err, r.drawnUnder(t, r.kid))
	}
}

// Review of F2: a parked proposal is shown to the owner, to whoever may
// adopt on its parent, and to the figaro that proposed it; not to another
// figaro on the same uid.
func TestAParkedProposalIsNotShownToAnotherFigaro(t *testing.T) {
	r := newOwnerRig(t, func(r ownerRig) []string {
		return []string{"*:aria:self", "*:aria:" + r.kid, "read:*"}
	})
	spy, _ := json.Marshal([]string{"read:aria:" + r.q})
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.holder, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: spy}, nil)})); err != nil {
		t.Fatal(err)
	}
	if err := r.arrange(t, r.self, r.kid, r.q); err == nil {
		t.Fatal("the shed did not park, so this test proves nothing")
	}
	list := func(figaro string) []rpc.Proposal {
		t.Helper()
		raw := withAria(t, r.q, rpc.ParkedRequest{FigaroID: r.q})
		if figaro != "" {
			raw = claimed(t, figaro, raw)
		}
		out, err := r.h[rpc.MethodParked](localOwner(), raw)
		if err != nil {
			t.Fatalf("parked as %q: %v", figaro, err)
		}
		return out.(rpc.ParkedResponse).Proposals
	}
	if got := list(r.holder); len(got) != 0 {
		t.Errorf("an unrelated figaro sees %d proposals parked on %s: %+v", len(got), r.q, got)
	}
	if got := list(r.self); len(got) != 1 {
		t.Errorf("the proposer sees %d of its own proposals", len(got))
	}
	if got := list(""); len(got) != 1 || got[0].Withheld != 0 {
		t.Errorf("the owner at the terminal sees %+v", got)
	}
}
