package aria

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func unitNode(n int) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", n)}
}

func unitPage(id uint64, nodes ...livedoc.Node) Page {
	return Page{Parts: []TurnPart{{Turn: Turn{ID: id, Sealed: true, Nodes: nodes}}}}
}

// A turn is unbounded, so a materialized message cannot be the turn. Cutting at
// node boundaries keeps every unit under the budget while losing nothing.
func TestUnits_BoundedWithoutLosingNodes(t *testing.T) {
	nodes := make([]livedoc.Node, 12)
	for i := range nodes {
		nodes[i] = unitNode(unitChars / 2)
	}
	got := NewClient().Apply(unitPage(7, nodes...), Quiet)
	if len(got) < 2 {
		t.Fatalf("a %d-char turn must split; got %d unit(s)", 12*(unitChars/2), len(got))
	}
	seen := 0
	for i, m := range got {
		if m.Turn != 7 {
			t.Errorf("unit %d: turn id = %d, want 7", i, m.Turn)
		}
		if m.From != uint64(seen) {
			t.Errorf("unit %d: From = %d, want %d: offsets must be contiguous", i, m.From, seen)
		}
		if len(m.Nodes) == 0 {
			t.Errorf("unit %d is empty", i)
		}
		seen += len(m.Nodes)
	}
	if seen != len(nodes) {
		t.Fatalf("units cover %d nodes, want %d", seen, len(nodes))
	}
}

// The smallest unit is one node: a node is never split.
func TestUnits_NeverSplitANode(t *testing.T) {
	got := NewClient().Apply(unitPage(1, unitNode(unitChars*3)), Quiet)
	if len(got) != 1 || len(got[0].Nodes) != 1 {
		t.Fatalf("a single oversized node must stay one unit; got %d units", len(got))
	}
	if len(got[0].Nodes[0].Markdown) != unitChars*3 {
		t.Fatal("the node was truncated; cutting must not alter payload")
	}
}

// Only the unit that starts the turn carries the question.
func TestUnits_OnlyTheHeadCarriesTheInquiry(t *testing.T) {
	p := unitPage(4, unitNode(unitChars/2+1), unitNode(unitChars/2+1), unitNode(8))
	p.Parts[0].Inquiry = "the question"
	got := NewClient().Apply(p, Quiet)
	if len(got) < 2 {
		t.Fatalf("fixture: want a split turn, got %d unit(s)", len(got))
	}
	if got[0].Inquiry != "the question" {
		t.Fatalf("the head unit lost the question: %q", got[0].Inquiry)
	}
	for _, m := range got[1:] {
		if m.Inquiry != "" {
			t.Fatalf("continuation unit at From=%d repeated the question", m.From)
		}
	}
}
