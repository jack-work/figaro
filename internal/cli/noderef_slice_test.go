package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A tall turn reaches the renderer as SEVERAL messages, cut at unit-size
// boundaries. A steered turn is the sharp case: the steer is a node INSIDE the
// agent's run, so the turn arrives as {From:0} steer + output, {From:2..} more
// output, and the inquiry rides on the first slice as TEXT, occupying no node
// id at all. Every node in that turn must own a DISTINCT nodeRef, because
// nodeRef keys expansion state (Ctrl-O) and the selection marks: two nodes
// sharing a ref share those, so expanding a tool would expand an unrelated node
// and a copy would take the wrong text.
//
// Minting refs from the SLICE-LOCAL index collides on {turn,0} twice over.
// Reverting nodeRefAt to `nodeRef{turn: m.Turn, index: i}` makes this report:
// "duplicate nodeRef {2 0}: nodes 0 and 1 of turn 2 share it".
func TestNodeRefIsUniquePerNodeAcrossTurnSlices(t *testing.T) {
	node := func(md string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeProse, Markdown: md}
	}
	// The exact shape I4 traced out of a real steered turn, minus the prompt
	// node it no longer has.
	slices := []aria.Message{
		{Turn: 2, From: 0, Inquiry: "the question", Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
			node("steer"), node("think"),
		}},
		{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
			node("prose"), node("more"), node("last"),
		}},
	}

	seen := map[nodeRef]string{}
	for _, m := range slices {
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			if prev, dup := seen[ref]; dup {
				t.Fatalf("duplicate nodeRef %v: %q and %q share it: "+
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
	if got := nodeRefAt(slices[1], 1); got.index != 3 {
		t.Errorf("node 1 of the {From:2} slice = index %d, want 3 (its turn position)", got.index)
	}
}

// The inquiry rides on the slice that STARTS a turn and nowhere else: node ids
// are positional, so a page that begins mid-turn (ClippedHead) would otherwise
// reprint the question above the middle of a reply.
func TestInquiryRidesOnlyOnTheFirstSliceOfATurn(t *testing.T) {
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "reply"}}

	head := committedMessages(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 2, Inquiry: "the question", Nodes: nodes}, From: 0},
	}})
	if len(head) != 1 || head[0].Inquiry != "the question" {
		t.Fatalf("first slice must carry the question, got %+v", head)
	}

	tail := committedMessages(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 2, Inquiry: "the question", Nodes: nodes}, From: 7, ClippedHead: true},
	}})
	if len(tail) != 1 || tail[0].Inquiry != "" {
		t.Fatalf("a clipped-head slice must NOT repeat the question, got %+v", tail)
	}
}
