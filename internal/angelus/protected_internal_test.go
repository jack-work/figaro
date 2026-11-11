package angelus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// The box door already hides an aria outside the box; this holds the
// protected-key rule on its own, should that reach ever widen.
func TestAnAncestorInABoxManagesOnlyDescendantsStillInIt(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	h := &handlers{angelus: &Angelus{Backend: backend}}
	in := func(id, sandbox string) {
		raw, _ := json.Marshal(sandbox)
		if _, err := backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	outfit, err := backend.CreateOutfit("o", form.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	in(parent, "judge")
	_, child, err := backend.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	box := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalSandbox, ID: authz.BoxPrincipal("judge").ID})
	claim, _ := rpc.WithCaller(map[string]any{}, parent, nil)
	var ctx context.Context
	_, _ = authz.Authenticated(authz.AriaHeader{}, func(c context.Context, _ json.RawMessage) (any, error) {
		ctx = c
		return nil, nil
	})(box, claim)
	if authz.FigaroOf(ctx) != parent {
		t.Fatalf("the claim did not stamp: %q", authz.FigaroOf(ctx))
	}
	c := callerKeys{boxed: true, box: "judge", lineage: true}
	check := func() error {
		snap, err := backend.FormState(child)
		if err != nil {
			t.Fatal(err)
		}
		return h.refuseProtected(ctx, c, lineage{of: child}, form.ScopesKey, snap)
	}
	if err := check(); err != nil {
		t.Fatalf("control: the parent manages a child in its box: %v", err)
	}
	in(child, "elsewhere")
	if err := check(); err == nil || !strings.Contains(err.Error(), "not in this box") {
		t.Fatalf("the old box's ancestor manages a moved child: %v", err)
	}
}

func TestAFigaroLeavingItsAncestorsCarriesTheirNarrowing(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	a := &Angelus{Backend: backend}
	a.Policies = authz.NewPolicies(jkauthzOwner(), StoreFacts(backend))
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	h := &handlers{angelus: a}
	grant := func(id string, gs ...string) {
		raw, _ := json.Marshal(gs)
		if _, err := backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	outfit, err := backend.CreateOutfit("o", form.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	top, err := backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	grant(top, "read:*", "egress:a:1")
	_, kid, err := backend.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.carryNarrowing(top); err != nil {
		t.Fatal(err)
	}
	if raw, _ := keyOf(t, backend, top); raw != `["read:*","egress:a:1"]` {
		t.Fatalf("a top-level figaro's grants were rewritten: %s", raw)
	}
	grant(kid, "*:*", "egress:*")
	if err := h.carryNarrowing(kid); err != nil {
		t.Fatal(err)
	}
	raw, _ := keyOf(t, backend, kid)
	if raw != `["read:*","egress:a:1"]` {
		t.Fatalf("the moved figaro carries %s", raw)
	}
	alone, _ := a.Policies.EffectiveAlone(kid)
	if alone.String() != "read:*, egress:a:1" {
		t.Fatalf("alone it holds %s", alone)
	}
}

func keyOf(t *testing.T, b *store.XwalBackend, id string) (string, bool) {
	t.Helper()
	snap, err := b.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := snap.Get(form.ScopesKey)
	return string(raw), ok
}

func jkauthzOwner() jkauthz.Principal { return jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"} }
