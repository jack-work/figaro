package aria

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A cold reader meets a long turn at its tail, then pages backward.
// Every returned node must remain visible whether the turn is sealed or live.
func TestClientBackfillsClippedTurn(t *testing.T) {
	for _, mode := range []string{"sealed", "live"} {
		t.Run(mode, func(t *testing.T) {
			s := NewServer()
			s.Restore([]Turn{
				{ID: 5, Inquiry: "inherited question", Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "inherited answer"}}},
				{ID: 6, Inquiry: "the child's long running turn"},
			})
			nodes := make([]livedoc.Node, 12)
			for i := range nodes {
				nodes[i] = livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("node %02d: %s", i, strings.Repeat("x", 300))}
			}
			// The real server reopens the same turn across model/tool rounds.
			s.OpenTurn(6)
			s.Update(nil, nodes[:6], 0)
			s.Close()
			s.OpenTurn(6)
			s.Update(nil, nodes, 0)
			sealed := mode == "sealed"
			if sealed {
				s.Close()
				s.Seal(nil)
			}

			const budget = 1024
			tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
			if len(tail.Parts) != 1 || !tail.Parts[0].ClippedHead || tail.Parts[0].Sealed != sealed || (tail.Parts[0].Live == nil) != sealed {
				t.Fatalf("fixture: expected a clipped %s tail, got %+v", mode, tail)
			}
			at, _ := tail.Span()
			older := s.ReadBefore(at, Anchor{}, budget)
			from, _ := older.Span()
			if len(older.Parts) != 1 || !from.Less(at) || len(older.Parts[0].Nodes) == 0 {
				t.Fatalf("fixture: backward read did not return earlier nodes: %+v", older)
			}
			t.Logf("tail starts at %v; backward read starts at %v", at, from)

			c := NewClient()
			c.Apply(tail, Quiet)
			c.Apply(older, Quiet)
			view := c.View()
			held := map[uint64]livedoc.Node{}
			messages := append([]Message(nil), view.Closed...)
			if view.Open != nil {
				messages = append(messages, *view.Open)
			}
			for _, m := range messages {
				if m.Turn == 6 {
					for i, n := range m.Nodes {
						held[m.From+uint64(i)] = n
					}
				}
			}
			for _, page := range []Page{older, tail} {
				for _, part := range page.Parts {
					for i, want := range part.Nodes {
						ordinal := part.From + uint64(i)
						if got, ok := held[ordinal]; !ok || !reflect.DeepEqual(got, want) {
							t.Fatalf("server returned node %d, but client view lost it after paging backward (tail began at %v)", ordinal, at)
						}
					}
				}
			}
		})
	}
}

// longNodes builds n distinguishable nodes, each too big for several to share
// a page, so the server really does clip.
func longNodes(n int) []livedoc.Node {
	out := make([]livedoc.Node, n)
	for i := range out {
		out[i] = livedoc.Node{Type: livedoc.NodeProse,
			Markdown: fmt.Sprintf("node %02d: %s", i, strings.Repeat("x", 300))}
	}
	return out
}

// A NODE REACHES THE PAGER ONCE, whatever order the pages arrive in: page
// backward into a live turn, page forward again over the same ground, keep
// streaming, then seal. A pager handed the same ordinal twice draws it twice,
// and the two copies are not even equal once a delta has edited the node.
//
// THIS IS A FACT TEST, NOT A CURSOR TEST, and the distinction cost an hour:
// Store.Insert subtracts what is already held, so it would hold this line
// green even if the fold offered every node three times. What the cursor
// itself does is measured by TestFoldOffersEachNodeOnceAcrossTheLiveBoundary
// below. Both are worth having: this one says the user is safe, that one says
// we are not paying for it with redundant work.
func TestClientBackfillThenForwardHandsNoNodeTwice(t *testing.T) {
	s := NewServer()
	s.Restore([]Turn{{ID: 5, Inquiry: "earlier", Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "earlier answer"}}}})
	s.OpenInquiry(6, "the long one", nil)
	nodes := longNodes(14)
	s.OpenTurn(6)
	s.Update(nil, nodes[:6], 0)
	s.Close()
	s.OpenTurn(6)
	s.Update(nil, nodes[:10], 0)

	c := NewClient()
	delivered := map[Anchor]int{}
	c.OnClosed = func(m Message) {
		for i := range m.Nodes {
			delivered[Anchor{Turn: uint64(m.Turn), Node: m.From + uint64(i)}]++
		}
	}

	const budget = 1024
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	if !tail.Parts[0].ClippedHead {
		t.Fatalf("fixture: the tail read was not clipped, so nothing is being tested: %+v", tail)
	}
	c.Apply(tail, Notify)
	at, _ := tail.Span()
	c.Apply(s.ReadBefore(at, Anchor{}, budget), Notify)
	// Forward again over ground already walked: the same nodes, re-offered.
	c.Apply(s.Read(at, budget), Notify)

	// The turn keeps running, then seals.
	s.Update(nil, nodes, 0)
	s.Close()
	s.Seal(nil)
	c.Apply(s.ReadBefore(Anchor{}, Anchor{}, 1<<20), Notify)

	for a, n := range delivered {
		if n != 1 {
			t.Errorf("node %v delivered %d times; the release cursor moved backward", a, n)
		}
	}
	// And the whole turn is there, once, in order.
	var got []string
	for _, m := range c.View().Closed {
		if m.Turn != 6 {
			continue
		}
		for _, n := range m.Nodes {
			got = append(got, n.Markdown)
		}
	}
	if len(got) != len(nodes) {
		t.Fatalf("closed view holds %d nodes of turn 6, want %d", len(got), len(nodes))
	}
	for i, want := range nodes {
		if got[i] != want.Markdown {
			t.Fatalf("node %d = %q, want %q", i, got[i], want.Markdown)
		}
	}
}

// THE OTHER HALF OF THE SAME BUG: a backward read can land BELOW Live.From,
// on nodes the server has already finished with. Those are closed, so they
// belong in the ranges; the open region starts at Live.From and will never
// reach down to them. Before the held floor existed they stayed in the open
// buffer with nothing addressing them at all.
//
// A resumed turn is what puts Live.From above zero: the turn already has nodes
// when it is reopened, so the streaming suffix starts above them.
func TestClientBackfillBelowTheLiveBoundary(t *testing.T) {
	nodes := longNodes(12)
	s := NewServer()
	s.Restore([]Turn{
		{ID: 5, Inquiry: "earlier", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "earlier answer"}}},
		{ID: 6, Inquiry: "resumed", Nodes: nodes[:8]},
	})
	s.OpenTurn(6)
	if s.open.from == 0 {
		t.Fatal("fixture: Live.From is 0, so the boundary case is not being tested")
	}
	s.Update(nil, nodes[8:], 0)

	const budget = 1024
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	at, _ := tail.Span()
	if at.Node <= s.open.from {
		t.Fatalf("fixture: the tail read began at %v, at or below Live.From %d", at, s.open.from)
	}
	older := s.ReadBefore(at, Anchor{}, budget)
	from, _ := older.Span()
	if !from.Less(Anchor{Turn: 6, Node: s.open.from}) {
		t.Fatalf("fixture: the backward read began at %v, not below Live.From %d", from, s.open.from)
	}

	c := NewClient()
	c.Apply(tail, Quiet)
	c.Apply(older, Quiet)

	// Everything the backward read handed back that sits below Live.From must
	// now be in the CLOSED view, not stranded in the open buffer.
	held := map[uint64]string{}
	for _, m := range c.View().Closed {
		if m.Turn != 6 {
			continue
		}
		for i, n := range m.Nodes {
			held[m.From+uint64(i)] = n.Markdown
		}
	}
	for _, part := range older.Parts {
		for i, want := range part.Nodes {
			ord := part.From + uint64(i)
			if ord >= s.open.from {
				continue
			}
			if got, ok := held[ord]; !ok || got != want.Markdown {
				t.Fatalf("node %d is below Live.From %d and was delivered, but the closed view does not hold it",
					ord, s.open.from)
			}
		}
	}
}

// THE SEAL OFFERS EACH NODE ONCE, after the turn has been paged backward and
// then forward again. Live.From is 0 for this turn's whole life, so nothing is
// released until it seals, and the seal is the single release this pins.
//
// IT IS NAMED FOR THAT AND NOT FOR THE CURSORS, because it cannot see them.
// Three sabotages of the cursor arithmetic were run against it and it stayed
// green through all three: openStart ignoring the release cursor, the reclaim
// release not advancing it, and openStart ignoring the held floor, which is
// the original bug. With no upward release there is nothing for the two
// cursors to disagree about, so openStart is barely consulted. A name that
// promised "each node once" in general would outlive this paragraph and be
// believed. The fixture that BITES is
// TestFoldOffersEachNodeOnceAcrossTheLiveBoundary below.
func TestSealOffersEachNodeOnceAfterABackfill(t *testing.T) {
	s := NewServer()
	s.Restore([]Turn{{ID: 5, Inquiry: "earlier", Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "earlier answer"}}}})
	s.OpenInquiry(6, "the long one", nil)
	nodes := longNodes(14)
	s.OpenTurn(6)
	s.Update(nil, nodes[:6], 0)
	s.Close()
	s.OpenTurn(6)
	s.Update(nil, nodes[:10], 0)

	c := NewClient()
	const budget = 1024
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	c.Apply(tail, Quiet)
	at, _ := tail.Span()
	c.Apply(s.ReadBefore(at, Anchor{}, budget), Quiet)
	c.Apply(s.Read(at, budget), Quiet)
	s.Update(nil, nodes, 0)
	s.Close()
	s.Seal(nil)
	c.Apply(s.ReadBefore(Anchor{}, Anchor{}, 1<<20), Quiet)

	// Turn 5's single node plus all 14 of turn 6, each offered once.
	if want := 1 + len(nodes); c.offered != want {
		t.Errorf("the fold offered %d nodes for release, want %d: a node was released twice "+
			"and Insert hid it", c.offered, want)
	}
}

// The same count, over the shape where the two releases MEET: a resumed turn
// puts Live.From above zero, a backward read lands below it, and the live
// frames that follow want to release the very run the backfill just released.
//
// This is the fixture that caught the first draft of the fix. It released the
// reclaimed run without advancing the cursor over it, so the next live frame
// offered [6,8) a second time: correct on screen, because Insert clipped it,
// and wrong in the only place that could tell.
//
// Proved able to fail, three ways, numbers measured on this fixture against 13
// nodes of legitimate work:
//
//	openStart ignores the release cursor ............ 24 offers
//	openStart ignores the held floor (the bug) ...... 21 offers
//	the reclaim release does not advance the cursor . 16 offers
func TestFoldOffersEachNodeOnceAcrossTheLiveBoundary(t *testing.T) {
	nodes := longNodes(12)
	s := NewServer()
	s.Restore([]Turn{
		{ID: 5, Inquiry: "earlier", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "earlier answer"}}},
		{ID: 6, Inquiry: "resumed", Nodes: nodes[:8]},
	})
	s.OpenTurn(6)
	s.Update(nil, nodes[8:11], 0)

	c := NewClient()
	const budget = 1024
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	at, _ := tail.Span()
	older := s.ReadBefore(at, Anchor{}, budget)
	from, _ := older.Span()
	if !from.Less(Anchor{Turn: 6, Node: s.open.from}) {
		t.Fatalf("fixture: the backward read began at %v, not below Live.From %d", from, s.open.from)
	}

	var live []Page
	cancel := s.Subscribe(func(p Page) { live = append(live, p) })
	defer cancel()

	c.Apply(tail, Quiet)
	c.Apply(older, Quiet)
	// A live frame over the same turn, after the backfill.
	s.Update(nil, nodes[8:], 0)
	if len(live) == 0 {
		t.Fatal("fixture: no live frame was pushed, so the second release is not being tested")
	}
	for _, p := range live {
		c.Apply(p, Quiet)
	}
	s.Close()
	s.Seal(nil)
	c.Apply(s.ReadBefore(Anchor{}, Anchor{}, 1<<20), Quiet)

	// Turn 5's node and all of turn 6's, each offered once. The final full
	// read legitimately backfills the nodes below the backward read's floor,
	// which is the first time anything has held them.
	if want := 1 + len(nodes); c.offered != want {
		t.Errorf("the fold offered %d nodes for release, want %d: the reclaimed run was "+
			"released and then offered again by the next live frame", c.offered, want)
	}
}
