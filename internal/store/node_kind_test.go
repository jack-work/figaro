package store

import (
	"testing"
)

func TestNodeKindNamesEveryTrunksKindWithoutScanningTheTopology(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedTree(t, b, 2, 2, 1)
	formID, _, err := b.CreateForm("", setPatch(map[string]string{"brief": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	all := b.store.Nodes()
	conv := b.store.Conversations()[0].ID
	if _, _, err := b.ForkWith(conv, 0, setPatch(map[string]string{"k": "v"})); err != nil {
		t.Fatal(err)
	}
	trunkScanCount.Store(0)
	sawForm := false
	for _, n := range all {
		kind, ok := b.NodeKind(n.ID)
		switch n.Kind {
		case KindConversation, KindForm:
			if !ok || kind != n.Kind {
				t.Errorf("NodeKind(%s) = %q, %t; Node says %q", n.ID, kind, ok, n.Kind)
			}
		default:
			if ok {
				t.Errorf("NodeKind(%s) = %q for a %s, which is no trunk", n.ID, kind, n.Kind)
			}
		}
		sawForm = sawForm || n.ID == formID
	}
	if !sawForm {
		t.Fatalf("form %s is not among the nodes", formID)
	}
	if _, ok := b.NodeKind("nosuchnode"); ok {
		t.Error("NodeKind found a node that does not exist")
	}
	if got := trunkScanCount.Load(); got != 0 {
		t.Fatalf("NodeKind after a birth scanned the topology %d times, want 0", got)
	}
}
