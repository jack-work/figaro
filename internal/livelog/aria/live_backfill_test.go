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
