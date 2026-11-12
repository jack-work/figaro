package angelus

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
	"github.com/jack-work/figaro/internal/store"
)

// C4: doctor normalize rewrites fork points and nothing else. A figaro drawn
// away from the narrowed one it answers to is detached, and every authority
// fact of every node (ancestry, effective scopes, protected-key writers,
// descent) reads the same afterwards.
func TestNormalizeChangesNoAuthority(t *testing.T) {
	root := t.TempDir()
	backend, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	if err := wire.Install(backend.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Backend: backend}
	a.Policies = authz.NewPolicies(jkauthzOwner(), StoreFacts(backend))
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	a.topology()
	grant := func(id string, gs ...string) {
		raw, _ := json.Marshal(gs)
		if _, err := backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	fork := func(of string) string {
		_, alt, err := backend.Fork(of)
		if err != nil {
			t.Fatal(err)
		}
		grant(alt, "*:*", "egress:*")
		return alt
	}
	outfit, err := backend.CreateOutfit("o", form.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	top, err := backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	grant(top, "*:*", "egress:*")
	narrowed := fork(top)
	grant(narrowed, "read:*", "egress:a:1")
	kid := fork(narrowed)
	grandkid := fork(kid)
	placed := fork(top)
	if err := hierarchytest.Of(backend).Move(placed, narrowed); err != nil {
		t.Fatalf("place a figaro under the narrowed one: %v", err)
	}
	if err := backend.HierarchyEngine().WriteDrawn(map[string]string{kid: "null", placed: top}, nil); err != nil {
		t.Fatal(err)
	}
	wide, _ := a.Policies.Effective(top)
	for _, id := range []string{kid, grandkid, placed} {
		if got, held := a.Policies.Effective(id); !held || got.String() == wide.String() {
			t.Fatalf("%s holds %s (held %v) before normalize: the fixture narrows nothing", id, got, held)
		}
	}

	before := authorityLines(t, backend)
	report, err := backend.Normalize(false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(report.Detached, kid) {
		t.Fatalf("normalize detached %v, want %s among them: the test would prove nothing", report.Detached, kid)
	}
	after := authorityLines(t, backend)
	if len(before) != len(after) {
		t.Fatalf("normalize changed the number of authority facts: %d before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("normalize changed an authority fact:\nbefore %safter  %s", before[i], after[i])
		}
	}
	if n, _ := backend.Node(kid); n.ForkPoint == narrowed || n.Parent != narrowed {
		t.Fatalf("after normalize the kid was cut from %s and answers to %s, want a new fork point and %s still", n.ForkPoint, n.Parent, narrowed)
	}
}
