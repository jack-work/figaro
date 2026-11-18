package angelus_test

import (
	"encoding/json"
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
	_ = r.arrange(t, r.self, r.kid, r.q)
	if got := r.drawnUnder(t, r.kid); got != r.self {
		t.Errorf("a figaro shed its child under %s, outside its lineage, without consent: the child is drawn under %q", r.q, got)
	}
	_ = r.arrange(t, r.self, r.self, r.holder)
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
