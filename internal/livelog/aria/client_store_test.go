package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// These assert the shim's side of the phase-1 contract: the store beneath
// Client holds what the doc says it holds, for the page sequences the client
// actually sees. The rest of this package's tests: untouched by the swap -
// assert that the OUTSIDE of the client is unchanged.

func liveDelta(turn uint64, from uint64, v int, id uint64, text string) Page {
	return Page{Parts: []TurnPart{{Turn: Turn{ID: turn, Live: &Live{
		From: from, V: v,
		Nodes: []NodeDelta{{ID: id, Set: map[string]any{
			"type": "prose", "role": "output", "markdown": text,
		}}},
	}}}}}
}

func sealed(turn uint64, inquiry string, n int) Page {
	t := Turn{ID: turn, Sealed: true, Inquiry: inquiry}
	for i := 0; i < n; i++ {
		t.Nodes = append(t.Nodes, livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "x"})
	}
	return Page{Parts: []TurnPart{{Turn: t, From: 0}}}
}

// TestOrdinaryAriaIsOneRange is the migration's central claim: "If nobody
// jumps and nothing is evicted, there is exactly one range forever and no gap
// ever renders. The model degenerates to today's behaviour."
func TestOrdinaryAriaIsOneRange(t *testing.T) {
	c := NewClient()
	for turn := uint64(1); turn <= 20; turn++ {
		c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: turn, Inquiry: "q"}}}})
		for k := uint64(0); k < 3; k++ {
			c.Apply(liveDelta(turn, 0, int(k)+1, k, "streamed"))
		}
		c.Apply(sealed(turn, "q", 3))
	}
	s := c.Store()
	if len(s.ranges) != 1 {
		t.Fatalf("an aria nobody jumped around in is ONE range; got %d: %v", len(s.ranges), s.ranges)
	}
	if got := len(c.View().Closed); got != 20 {
		t.Fatalf("20 turns, 20 closed messages; got %d", got)
	}
	segs := s.Query(Anchor{Turn: 1}, Anchor{Turn: 20, Node: 2})
	if len(segs) != 1 || segs[0].Gap != nil {
		t.Fatalf("no gap may render; got %d segments", len(segs))
	}
}

// TestReleasedHeadLandsInTheHeadRange is invariant 4: nodes below Live.From
// are RELEASED INTO the ranges, not held in both places.
func TestReleasedHeadLandsInTheHeadRange(t *testing.T) {
	c := NewClient()
	c.Apply(liveDelta(1, 0, 1, 0, "first"))
	c.Apply(liveDelta(1, 0, 2, 1, "second"))
	// The suffix boundary advances: node 0 is closed for good.
	c.Apply(liveDelta(1, 1, 3, 1, "second again"))

	s := c.Store()
	if s.Count() != 1 {
		t.Fatalf("the released head is one closed message; store holds %d", s.Count())
	}
	if got := s.ranges[0]; got.From != (Anchor{Turn: 1, Node: 0}) || got.To != (Anchor{Turn: 1, Node: 0}) {
		t.Fatalf("head range covers %v..%v, want just node 0", got.From, got.To)
	}
	open := c.Open()
	if open == nil || open.From != 1 {
		t.Fatalf("the open suffix starts where the ranges stop; got %#v", open)
	}
	if len(open.Nodes) != 1 {
		t.Fatalf("the suffix is node 1 alone; got %d nodes", len(open.Nodes))
	}
}

// TestCatchUpOverlapDoesNotDoubleApply: the same turn arriving twice, once
// live and once as history, must not be held twice.
func TestCatchUpOverlapDoesNotDoubleApply(t *testing.T) {
	c := NewClient()
	c.Apply(liveDelta(1, 0, 1, 0, "hello"))
	c.Apply(sealed(1, "q", 1))
	before := len(c.View().Closed)

	// A catch-up read restating the same turn.
	c.Apply(sealed(1, "q", 1))
	if got := len(c.View().Closed); got != before {
		t.Fatalf("re-applying a sealed turn changed the view: %d -> %d", before, got)
	}
	s := c.Store()
	seen := map[Anchor]int{}
	for _, r := range s.ranges {
		for _, m := range r.Msgs {
			f, to := msgSpan(m)
			for a := f; !to.Less(a); a = a.Next() {
				seen[a]++
				if seen[a] > 1 {
					t.Fatalf("anchor %v held twice", a)
				}
			}
		}
	}
}

// TestRetentionTrimsFromTheBottomOnly pins today's semantics: trimming is a
// prefix drop, so it cannot open a hole, and closedFloor moves with it.
func TestRetentionTrimsFromTheBottomOnly(t *testing.T) {
	c := NewClient()
	c.SetClosedLimit(5)
	for turn := uint64(1); turn <= 12; turn++ {
		c.Apply(sealed(turn, "q", 1))
	}
	v := c.View()
	if len(v.Closed) != 5 {
		t.Fatalf("limit 5, got %d", len(v.Closed))
	}
	if v.Closed[0].Turn != 8 || v.Closed[4].Turn != 12 {
		t.Fatalf("kept the wrong window: %d..%d", v.Closed[0].Turn, v.Closed[4].Turn)
	}
	s := c.Store()
	if len(s.ranges) != 1 {
		t.Fatalf("a prefix drop cannot open a hole; got %d ranges", len(s.ranges))
	}
	if !c.seenClosed(3) {
		t.Fatal("a trimmed-away turn is still below the floor and must read as seen")
	}
}
