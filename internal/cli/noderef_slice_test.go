package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A turn reaches the renderer as SEVERAL messages, cut at voice-run boundaries.
// A steered turn is the sharp case: it arrives as {From:0} inquiry,
// {From:1} steer, {From:2..} output. Every node in that turn must own a
// DISTINCT nodeRef, because nodeRef keys expansion state (Ctrl-O) and the
// selection marks — two nodes sharing a ref share those, so expanding a tool
// would expand an unrelated node and a copy would take the wrong text.
//
// Minting refs from the SLICE-LOCAL index collides on {turn,0} three times
// over. Reverting nodeRefAt to `nodeRef{turn: m.Turn, index: i}` makes this
// report: "duplicate nodeRef {2 0}: nodes 0 and 1 of turn 2 share it".
func TestNodeRefIsUniquePerNodeAcrossTurnSlices(t *testing.T) {
	node := func(md string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeProse, Markdown: md}
	}
	// The exact shape I4 traced out of a real steered turn.
	slices := []aria.Message{
		{Turn: 2, From: 0, Role: livedoc.RoleInput, Nodes: []livedoc.Node{node("inquiry")}},
		{Turn: 2, From: 1, Role: livedoc.RoleInput, Nodes: []livedoc.Node{node("steer")}},
		{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
			node("think"), node("prose"), node("more"),
		}},
	}

	seen := map[nodeRef]string{}
	for _, m := range slices {
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			if prev, dup := seen[ref]; dup {
				t.Fatalf("duplicate nodeRef %v: %q and %q share it — "+
					"slice-local indices collide across the slices of one turn",
					ref, prev, n.Markdown)
			}
			seen[ref] = n.Markdown
		}
	}

	if len(seen) != 5 {
		t.Fatalf("got %d distinct refs, want 5 (one per node in the turn)", len(seen))
	}
	// The ref index must be the node's position in the TURN, matching the
	// wire's guarantee that Nodes[i].ID == From+i.
	if got := nodeRefAt(slices[2], 1); got.index != 3 {
		t.Errorf("node 1 of the {From:2} slice = index %d, want 3 (its turn position)", got.index)
	}
}
