package compose

import (
	"reflect"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// The inquiry is the ONE node the streaming projection never emits — Nodes()
// walks agent messages — so the engine used to hand-build it as
// {Type, Role, Markdown}. The composed path built the same node with ID, LTs,
// Src and At as well, so the same question rendered with full provenance if you
// re-read it and with none if you watched it arrive.
//
// PromptNodes is now the single constructor. This pins both halves of that:
// the node is fully formed, and Turns produces byte-identical output.
func TestPromptNodeIsFullyFormedAndSingleSourced(t *testing.T) {
	m := message.Message{
		Role:        message.RoleInput,
		LogicalTime: 7,
		Timestamp:   1785029364891,
		TurnID:      3,
		Content:     []message.Content{prose("the question")},
	}

	got := PromptNodes(m)
	if len(got) != 1 {
		t.Fatalf("PromptNodes gave %d nodes, want 1", len(got))
	}
	n := got[0]

	// Exactly the fields the hand-built node was missing.
	if n.ID == "" {
		t.Error("no ID — a hand-built prompt node had none")
	}
	if len(n.LTs) != 1 || n.LTs[0] != 7 {
		t.Errorf("LTs = %v, want [7]", n.LTs)
	}
	if len(n.Src) != 1 || n.Src[0] != (livedoc.Src{LT: 7, Block: 0}) {
		t.Errorf("Src = %v, want [{7 0}]", n.Src)
	}
	if n.At != m.Timestamp {
		t.Errorf("At = %d, want %d", n.At, m.Timestamp)
	}
	if n.Role != livedoc.RoleInput || n.Type != livedoc.NodeProse {
		t.Errorf("node = %s/%s, want prose/input", n.Type, n.Role)
	}

	// And the composed path uses the same constructor, so the live node and the
	// re-read node cannot drift apart again.
	turns := Turns([]message.Message{m, asstLT(8, prose("the answer"))}, nil, nil)
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if !reflect.DeepEqual(turns[0].Nodes[0], n) {
		t.Errorf("composed prompt node differs from the live one:\n composed %+v\n live     %+v",
			turns[0].Nodes[0], n)
	}
}
