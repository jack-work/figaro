package aria

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A clone that dropped turns says there is more above; the child's complete
// tail page must be believed over it, or Query reports a trailing hole whose
// fill anchor wraps to zero and re-reads the tail forever.
func TestNoTrailingHoleAfterACloneAndACompleteTail(t *testing.T) {
	parent := NewClient()
	parent.Apply(Page{Parts: sealedParts(1, 5, "PARENT")}, Notify)

	child := parent.CloneBelow(3)
	if !child.Store().More().After {
		t.Fatal("a clone that dropped turns 3 to 5 must say there is more above what it kept")
	}

	child.Apply(Page{Parts: sealedParts(3, 2, "CHILD"), More: More{Before: true}}, Notify)

	if child.Store().More().After {
		t.Fatal("the store still claims there is content above a page that said it is the tail")
	}
	top, ok := child.TailFrom(1)
	if !ok {
		t.Fatal("the child holds nothing")
	}
	for _, seg := range child.Query(Anchor{Turn: 1}, Anchor{Turn: ^uint64(0), Node: ^uint64(0)}) {
		if seg.Gap != nil {
			t.Fatalf("a hole at %v..%v above a conversation the client holds whole (tail %v)",
				seg.Gap.From, seg.Gap.To, top)
		}
	}
}

// A page from the middle says nothing about the tail: its More.After may not
// clear a store that really does have more above.
func TestAMiddlePageDoesNotClearTheTail(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: sealedParts(5, 3, "TAIL"), More: More{Before: true, After: true}}, Notify)
	if !c.Store().More().After {
		t.Fatal("fixture: the page said there is more above and the store did not take it")
	}
	c.Apply(Page{Parts: sealedParts(1, 2, "OLDER"), More: More{After: false}}, Notify)
	if !c.Store().More().After {
		t.Fatal("a page of older history cleared what the store knew about the tail")
	}
}

func sealedParts(first, n int, tag string) []TurnPart {
	var parts []TurnPart
	for i := range n {
		id := uint64(first + i)
		parts = append(parts, TurnPart{Turn: Turn{
			ID: id, Inquiry: tag, Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("%s-%d", tag, id)}},
		}})
	}
	return parts
}

// A page clipped inside a turn the store holds whole speaks for nothing: the
// edge authority is the store's covered top, not the last message's start.
func TestAClippedPageDoesNotSpeakForTheTail(t *testing.T) {
	c := NewClient()
	whole := TurnPart{Turn: Turn{
		ID: 1, Inquiry: "q", Sealed: true,
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "node zero"},
			{Type: livedoc.NodeProse, Markdown: "node one"},
		},
	}}
	c.Apply(Page{Parts: []TurnPart{whole}}, Notify)

	clipped := whole
	clipped.Nodes = whole.Nodes[:1]
	clipped.ClippedTail = true
	c.Apply(Page{Parts: []TurnPart{clipped}, More: More{After: true}}, Notify)

	if c.Store().More().After {
		t.Fatal("a page clipped inside a turn the store holds whole was allowed to speak for the tail")
	}
	for _, seg := range c.Query(Anchor{Turn: 1}, Anchor{Turn: ^uint64(0), Node: ^uint64(0)}) {
		if seg.Gap != nil {
			t.Fatalf("phantom hole at %v..%v over a turn the store holds whole", seg.Gap.From, seg.Gap.To)
		}
	}
}

// A page's span is its extrema, not its first and last element.
func TestPageSpanIsTheExtremaNotTheEnds(t *testing.T) {
	parts := sealedParts(1, 4, "T")
	up := Page{Parts: parts}
	down := Page{Parts: append([]TurnPart(nil), parts...)}
	for i, j := 0, len(down.Parts)-1; i < j; i, j = i+1, j-1 {
		down.Parts[i], down.Parts[j] = down.Parts[j], down.Parts[i]
	}
	lo, hi := up.Span()
	rlo, rhi := down.Span()
	if lo != rlo || hi != rhi {
		t.Fatalf("reversed parts span %v..%v, ascending %v..%v", rlo, rhi, lo, hi)
	}

	c := NewClient()
	c.Apply(Page{Parts: sealedParts(1, 4, "T")}, Notify)
	c.Apply(Page{Parts: down.Parts, More: More{After: true}}, Notify)
	if !c.Store().More().After {
		t.Fatal("a reversed page that reaches the tail was not allowed to say what lies above it")
	}
}
