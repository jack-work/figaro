package store_test

// The delete a lifetime performs, against the presentation hierarchy.

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func ttlPresentBackend(t *testing.T) (*store.XwalBackend, string) {
	t.Helper()
	root := t.TempDir()
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	outfit, err := b.CreateOutfit("ttl", message.Patch{Set: map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b, outfit
}

func ttlIDs(b *store.XwalBackend) map[string]bool {
	live := map[string]bool{}
	for _, n := range b.Nodes() {
		live[n.ID] = true
	}
	return live
}

// An expired ancestor takes its branches however new they are: Gluck's rule,
// and the reason the sweep deletes recursively.
func TestExpiredAncestorTakesNewerBranches(t *testing.T) {
	b, outfit := ttlPresentBackend(t)
	parent, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	_, child, err := b.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(parent, true); err != nil {
		t.Fatalf("recursive remove: %v", err)
	}
	live := ttlIDs(b)
	if live[parent] || live[child] {
		t.Fatalf("parent %v / child %v still present; a lifetime must take the subtree",
			live[parent], live[child])
	}
}

// A non-recursive delete of a parent is refused, which is why the sweep never
// asks for one: the refusal would leave the expired node alive forever.
func TestNonRecursiveRemoveOfAParentIsRefused(t *testing.T) {
	b, outfit := ttlPresentBackend(t)
	parent, _ := b.CreateConversation(outfit)
	if _, _, err := b.Fork(parent); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(parent, false); err == nil {
		t.Fatal("a non-recursive delete of a node with branches must be refused")
	}
}

// THE PROMOTION RULE. A child promoted past its former parent is no longer
// presented under it, so an expired parent's delete set no longer contains the
// child: the child survives, and the delete's boundary repair detaches it --
// disk normalisation, forced exactly when it is owed.
func TestPromotedChildSurvivesItsExpiredFormerParent(t *testing.T) {
	b, outfit := ttlPresentBackend(t)
	parent, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	_, child, err := b.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Promote(child, 1); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// State the promotion in the terms the delete reads. Parent is LINEAGE
	// and a promote never moves it; Present is where the row is drawn, and
	// after this the drawing is inverted: the former parent hangs under its
	// own child.
	for _, n := range b.Nodes() {
		switch n.ID {
		case child:
			if n.Present == parent {
				t.Fatalf("child %s is still drawn under %s; the promotion did not take",
					child, parent)
			}
		case parent:
			if n.Present != child {
				t.Fatalf("former parent %s is drawn under %q, want its promoted child %s",
					parent, n.Present, child)
			}
		}
	}

	if err := b.Remove(parent, true); err != nil {
		t.Fatalf("removing the promoted-past parent: %v", err)
	}
	live := ttlIDs(b)
	if live[parent] {
		t.Errorf("the expired former parent %s survived its own deletion", parent)
	}
	if !live[child] {
		t.Fatalf("the promoted child %s was taken with its former parent", child)
	}
	// And it is still readable: the boundary repair must have given it its
	// own prefix rather than leaving it pointing at deleted bytes.
	if _, err := b.FormState(child); err != nil {
		t.Errorf("the promoted survivor cannot be read after the delete: %v", err)
	}
	if _, err := b.OpenFigIR(child); err != nil {
		t.Errorf("the promoted survivor's IR cannot be opened after the delete: %v", err)
	}
}
