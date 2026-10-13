package aria

import (
	"testing"

	client "github.com/jack-work/figaro/sdk/aria"
)

// A backward read into a running turn ends below the streaming suffix with
// More.After true; the open tail is coverage and must not become a hole.
func TestClient_PrefixPageDoesNotOpenAHoleOverTheOpenTurn(t *testing.T) {
	s := NewServer()
	s.Restore([]Turn{{ID: 5, Inquiry: "earlier", Sealed: true, Nodes: longNodes(1)}})
	s.OpenInquiry(6, "the long one", nil)
	s.OpenTurn(6)
	s.Update(nil, longNodes(14), 0)

	const budget = 1024
	c := client.NewClient()
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	c.Apply(tail, client.Quiet)
	if c.MoreAfter() {
		t.Fatalf("fixture: the tail read did not leave us at the tail")
	}
	at, _ := tail.Span()
	older := s.ReadBefore(at, Anchor{}, budget)
	if !older.More.After {
		t.Fatalf("fixture: the backward page does not claim more above it, so nothing is tested")
	}

	c.Apply(older, client.Quiet)
	if c.MoreAfter() {
		t.Errorf("a page ending at %v opened a hole above it, over the open turn's own "+
			"suffix [%d,%d) which we hold", at, c.Store().OpenHead(), c.Store().OpenLen())
	}
}

// A page that really does stop below the tail must still be able to say so.
func TestClient_AHistoryPageStillReportsMoreAbove(t *testing.T) {
	s := NewServer()
	turns := []Turn{{ID: 5, Inquiry: "earlier", Sealed: true, Nodes: longNodes(1)}}
	for id := 6; id <= 12; id++ {
		turns = append(turns, Turn{ID: uint64(id), Inquiry: "q", Sealed: true, Nodes: longNodes(3)})
	}
	s.Restore(turns)

	const budget = 512
	c := client.NewClient()
	mid := s.ReadBefore(Anchor{Turn: 9, Node: 0}, Anchor{}, budget)
	if !mid.More.After {
		t.Fatalf("fixture: a page cut at turn 9 of 12 must report more above it: %+v", mid.More)
	}
	c.Apply(mid, client.Quiet)
	if !c.MoreAfter() {
		t.Error("a genuine mid-history page was not allowed to report the conversation above it")
	}
}
