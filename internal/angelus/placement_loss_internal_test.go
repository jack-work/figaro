package angelus

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
	"github.com/jack-work/figaro/internal/store"
)

// P4: a figaro placed under a narrowed one answers to it. When that one is
// deleted, the survivor keeps exactly what it could do, carried onto its own
// board before the placement goes, and it does not fall back under the node
// its history was cut from.
func TestAFigaroLosingItsPlacementParentKeepsItsNarrowing(t *testing.T) {
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
	a.topology()
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
	grant(top, "*:*", "egress:*")
	_, narrowed, err := backend.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	grant(narrowed, "read:*", "egress:a:1")
	_, kid, err := backend.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	grant(kid, "*:*", "egress:*")
	wide, _ := a.Policies.Effective(kid)

	tree := hierarchytest.Of(backend)
	if err := tree.Move(kid, narrowed); err != nil {
		t.Fatalf("place the kid under the narrowed figaro: %v", err)
	}
	n, _ := backend.Node(kid)
	if n.Parent != narrowed || n.ForkPoint != top {
		t.Fatalf("the kid answers to %s and was cut from %s, want %s and %s", n.Parent, n.ForkPoint, narrowed, top)
	}
	want, held := a.Policies.Effective(kid)
	if !held || want.String() == wide.String() {
		t.Fatalf("placed under the narrowed figaro the kid holds %s, as wide as before (%s)", want, wide)
	}
	if err := tree.Arrange(map[string]string{kid: top}, nil); err != nil {
		t.Fatalf("draw the kid under the top so the delete spares it: %v", err)
	}
	if n, _ := backend.Node(kid); n.Parent != narrowed {
		t.Fatalf("drawing the kid elsewhere moved whom it answers to: %s", n.Parent)
	}

	if err := tree.Remove(narrowed, false); err != nil {
		t.Fatalf("delete the narrowed figaro: %v", err)
	}
	got, held := a.Policies.Effective(kid)
	if !held || got.String() != want.String() {
		t.Fatalf("after its placement parent was deleted the kid holds %s (held %v), want %s unchanged", got, held, want)
	}
	n, _ = backend.Node(kid)
	if n.Unplaced || n.Parent == top || n.Parent == narrowed {
		t.Fatalf("the kid answers to %q (unplaced %v): want nobody, not its fork point and not the deleted node", n.Parent, n.Unplaced)
	}
	if n.ForkPoint != top {
		t.Fatalf("the kid's fork point is %s, want %s untouched", n.ForkPoint, top)
	}
}
