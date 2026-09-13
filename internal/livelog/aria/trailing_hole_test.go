package aria

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// THE 494-READ LOOP, at its source. The bench drove four hops and caught 494
// history reads at the zero anchor, one every 24 ms for 45 idle seconds. The
// reviewer (27068b2c) reduced it to this: a parent, a clone that drops turns,
// and the child's complete tail page.
//
// Three things had to be true at once, and each is now a law of its own:
//
//  1. the clone says "there is more above what I kept", which is honest;
//  2. the child's page says there is not, and NOTHING CONSUMED THAT, so the
//     store went on believing the clone;
//  3. so Query answered with a trailing hole running to the top of the
//     coordinate space, whose fill anchor WRAPPED to the zero anchor, which
//     this wire reads as "the tail". Every frame re-read the tail, changed
//     nothing, and asked again.
func TestNoTrailingHoleAfterACloneAndACompleteTail(t *testing.T) {
	parent := NewClient()
	parent.Apply(Page{Parts: sealedParts(1, 5, "PARENT")}, Notify)

	child := parent.CloneBelow(3)
	if !child.Store().More().After {
		t.Fatal("a clone that dropped turns 3 to 5 must say there is more above what it kept")
	}

	// The child's own tail, complete: turns 3 and 4, nothing above them.
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

// A PAGE FROM THE MIDDLE SAYS NOTHING ABOUT THE TAIL. The rule above must not
// become "the last page wins": a history read of turns 1 and 2, whose own
// More.After is false because there is nothing above it ON THAT PAGE, may not
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

// A PAGE CLIPPED INSIDE A TURN THE STORE HOLDS WHOLE SPEAKS FOR NOTHING.
//
// The reviewer's flag-aware model (27068b2c, seed [1,0,0, 0,1,0, 0,0,1]) found
// this one against the first cut of the rule above: the store holds one turn
// of two nodes, a valid page arrives carrying only node 0 of that same turn,
// clipped at the tail, saying there is more above it, which is TRUE of the
// page. The edge authority was the last message's START anchor, so the page
// looked like the tail, its claim was adopted, and a phantom hole appeared
// over the node the store had held all along.
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

// A PAGE'S SPAN IS ITS EXTREMA. Parts ascending is a wire invariant, but the
// range a page covers is a statement about coordinates rather than about slice
// positions, and the difference is invisible until something reverses them.
// The reviewer's model does (seed [48,158,48]): payload coverage stayed
// correct while the edge decision read the last ELEMENT, which was an earlier
// turn than the store's top, so a page that really did reach the tail was not
// allowed to say so.
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

	// And the edge decision that reads it follows: a reversed page that
	// reaches the tail may still speak for it.
	c := NewClient()
	c.Apply(Page{Parts: sealedParts(1, 4, "T")}, Notify)
	c.Apply(Page{Parts: down.Parts, More: More{After: true}}, Notify)
	if !c.Store().More().After {
		t.Fatal("a reversed page that reaches the tail was not allowed to say what lies above it")
	}
}
