package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// nodeRef keys expansion state and selection marks, so every node of a turn
// split across slices must own a distinct one; the slice-local index collides.
func TestNodeRefIsUniquePerNodeAcrossTurnSlices(t *testing.T) {
	node := func(md string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeProse, Markdown: md}
	}
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
	// The wire guarantees Nodes[i].ID == From+i.
	if got := nodeRefAt(slices[1], 1); got.index != 3 {
		t.Errorf("node 1 of the {From:2} slice = index %d, want 3 (its turn position)", got.index)
	}
}

// A page that begins mid-turn must not reprint the question.
func TestInquiryRidesOnlyOnTheFirstSliceOfATurn(t *testing.T) {
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "reply"}}

	head := pageMessages(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 2, Inquiry: "the question", Sealed: true, Nodes: nodes}, From: 0},
	}})
	if len(head) != 1 || head[0].Inquiry != "the question" {
		t.Fatalf("first slice must carry the question, got %+v", head)
	}

	tail := pageMessages(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 2, Inquiry: "the question", Sealed: true, Nodes: nodes}, From: 7, ClippedHead: true},
	}})
	if len(tail) != 1 || tail[0].Inquiry != "" {
		t.Fatalf("a clipped-head slice must not repeat the question, got %+v", tail)
	}
}
