package aria

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	client "github.com/jack-work/figaro/sdk/aria"
)

// A cold reader meets a long turn at its tail, then pages backward; every
// returned node must remain visible whether the turn is sealed or live.
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

			c := client.NewClient()
			c.Apply(tail, client.Quiet)
			c.Apply(older, client.Quiet)
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

// longNodes builds n nodes each too big for several to share a page.
func longNodes(n int) []livedoc.Node {
	out := make([]livedoc.Node, n)
	for i := range out {
		out[i] = livedoc.Node{Type: livedoc.NodeProse,
			Markdown: fmt.Sprintf("node %02d: %s", i, strings.Repeat("x", 300))}
	}
	return out
}

// A node reaches the pager once, whatever order the pages arrive in. This
// checks the view; TestFoldOffersEachNodeOnceAcrossTheLiveBoundary checks
// that the fold did not offer it twice.
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

	c := client.NewClient()
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
	c.Apply(tail, client.Notify)
	at, _ := tail.Span()
	c.Apply(s.ReadBefore(at, Anchor{}, budget), client.Notify)
	c.Apply(s.Read(at, budget), client.Notify)

	s.Update(nil, nodes, 0)
	s.Close()
	s.Seal(nil)
	c.Apply(s.ReadBefore(Anchor{}, Anchor{}, 1<<20), client.Notify)

	for a, n := range delivered {
		if n != 1 {
			t.Errorf("node %v delivered %d times; the release cursor moved backward", a, n)
		}
	}
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

// A backward read can land below Live.From, on nodes the server has already
// closed; those belong in the ranges, not the open buffer.
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

	c := client.NewClient()
	c.Apply(tail, client.Quiet)
	c.Apply(older, client.Quiet)

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

// The seal offers each node once after the turn has been paged backward and
// forward. Live.From stays 0 here, so the seal is the single release.
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

	c := client.NewClient()
	const budget = 1024
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	c.Apply(tail, client.Quiet)
	at, _ := tail.Span()
	c.Apply(s.ReadBefore(at, Anchor{}, budget), client.Quiet)
	c.Apply(s.Read(at, budget), client.Quiet)
	s.Update(nil, nodes, 0)
	s.Close()
	s.Seal(nil)
	c.Apply(s.ReadBefore(Anchor{}, Anchor{}, 1<<20), client.Quiet)

	if want := 1 + len(nodes); c.Offered() != want {
		t.Errorf("the fold offered %d nodes for release, want %d: a node was released twice "+
			"and Insert hid it", c.Offered(), want)
	}
}

// The same count where the two releases meet: a resumed turn puts Live.From
// above zero, a backward read lands below it, and the live frames that
// follow must not re-offer the run the backfill released.
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

	c := client.NewClient()
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

	c.Apply(tail, client.Quiet)
	c.Apply(older, client.Quiet)
	s.Update(nil, nodes[8:], 0)
	if len(live) == 0 {
		t.Fatal("fixture: no live frame was pushed, so the second release is not being tested")
	}
	for _, p := range live {
		c.Apply(p, client.Quiet)
	}
	s.Close()
	s.Seal(nil)
	c.Apply(s.ReadBefore(Anchor{}, Anchor{}, 1<<20), client.Quiet)

	// The final full read legitimately backfills the nodes below the backward
	// read's floor.
	if want := 1 + len(nodes); c.Offered() != want {
		t.Errorf("the fold offered %d nodes for release, want %d: the reclaimed run was "+
			"released and then offered again by the next live frame", c.Offered(), want)
	}
}
