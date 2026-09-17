package aria

import "testing"

// A PREFIX PAGE MUST NOT DECLARE A HOLE OVER THE LIVE SUFFIX.
//
// More.After is a claim about what lies above a page's last node. A backward
// read into a running turn ends below the streaming suffix, so its After is
// true, and truthfully: there IS more above it. But we are holding that more,
// in the open tail, which adoptMoreAfter did not count as coverage because
// coverage came from Store.Top() and the open tail is not a range.
//
// So the store came to believe in a hole above a tail it already had. That is
// the 494-read loop by another road: the pager reads to fill the hole, the
// read returns what it holds, nothing changes, and it reads again on the next
// frame, forever.
func TestClient_PrefixPageDoesNotOpenAHoleOverTheOpenTurn(t *testing.T) {
	s := NewServer()
	s.Restore([]Turn{{ID: 5, Inquiry: "earlier", Sealed: true, Nodes: longNodes(1)}})
	s.OpenInquiry(6, "the long one", nil)
	s.OpenTurn(6)
	s.Update(nil, longNodes(14), 0)

	const budget = 1024
	c := NewClient()
	tail := s.ReadBefore(Anchor{}, Anchor{}, budget)
	c.Apply(tail, Quiet)
	if c.MoreAfter() {
		t.Fatalf("fixture: the tail read did not leave us at the tail")
	}
	at, _ := tail.Span()
	older := s.ReadBefore(at, Anchor{}, budget)
	if !older.More.After {
		t.Fatalf("fixture: the backward page does not claim more above it, so nothing is tested")
	}

	c.Apply(older, Quiet)
	if c.MoreAfter() {
		t.Errorf("a page ending at %v opened a hole above it, over the open turn's own "+
			"suffix [%d,%d) which we hold", at, c.Store().OpenHead(), c.Store().OpenLen())
	}
}

// And the converse, so the coverage claim cannot simply be "always covered":
// a page that really does stop below the tail must still be able to say so.
func TestClient_AHistoryPageStillReportsMoreAbove(t *testing.T) {
	s := NewServer()
	turns := []Turn{{ID: 5, Inquiry: "earlier", Sealed: true, Nodes: longNodes(1)}}
	for id := 6; id <= 12; id++ {
		turns = append(turns, Turn{ID: uint64(id), Inquiry: "q", Sealed: true, Nodes: longNodes(3)})
	}
	s.Restore(turns)

	const budget = 512
	c := NewClient()
	mid := s.ReadBefore(Anchor{Turn: 9, Node: 0}, Anchor{}, budget)
	if !mid.More.After {
		t.Fatalf("fixture: a page cut at turn 9 of 12 must report more above it: %+v", mid.More)
	}
	c.Apply(mid, Quiet)
	if !c.MoreAfter() {
		t.Error("a genuine mid-history page was not allowed to report the conversation above it")
	}
}
