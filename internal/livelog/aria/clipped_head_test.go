package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// A catch-up read of a turn TOO BIG FOR ONE PAGE comes back clipped at the
// head: the part starts at node From>0 and — correctly — carries no inquiry,
// because the slice that starts the turn is the only one that may.
//
// The client used to promote such a part as a message starting at node ZERO.
// absorb pads the slots below From so the ids stay positional, and the seal
// path then handed those padding slots out as a HEAD SLICE. A head slice is
// where every renderer draws the question — and this one had none. That is the
// whole of the user's report: "the inquiry doesn't always appear, even past
// inquiries", where the something-he-could-not-put-his-finger-on was the page
// budget, i.e. how big the turn happened to be.
func TestClient_ClippedPartDoesNotFabricateAHeadSlice(t *testing.T) {
	c := NewClient()
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "the tail of a long turn"}}
	c.Apply(Page{Parts: []TurnPart{{
		Turn:        Turn{ID: 7, Inquiry: "the question", Sealed: true, Nodes: nodes},
		From:        1,
		ClippedHead: true,
	}}})

	closed := c.View().Closed
	if len(closed) != 1 {
		t.Fatalf("closed = %d messages, want 1: %+v", len(closed), closed)
	}
	m := closed[0]
	if m.From != 1 {
		t.Errorf("From = %d, want 1 — the slice starts where the part did", m.From)
	}
	if len(m.Nodes) != 1 || m.Nodes[0].Markdown != nodes[0].Markdown {
		t.Errorf("nodes = %+v, want just the clipped part's own node", m.Nodes)
	}
	if m.Inquiry != "" {
		t.Errorf("inquiry = %q on a clipped slice: it would print the question mid-turn", m.Inquiry)
	}
}

// The complement: when the head IS delivered, the slice that starts the turn
// carries the question, so scrolling back to it shows what a mid-turn page
// deliberately withheld.
func TestClient_HeadPartCarriesTheInquiry(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Inquiry: "the question", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "head"}}},
	}}})
	closed := c.View().Closed
	if len(closed) != 1 || closed[0].From != 0 || closed[0].Inquiry != "the question" {
		t.Fatalf("head slice = %+v, want From 0 carrying the question", closed)
	}
}

// A clipped catch-up read of the turn STILL STREAMING must not put the padding
// slots into the live region either: the open message is the part of the turn
// we actually hold, and it does not start the turn, so it draws no question.
func TestClient_ClippedCatchUpKeepsTheOpenRegionHonest(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 9, Inquiry: "the question", Live: &Live{From: 0, V: 0},
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "tail"}}},
		From:        3,
		ClippedHead: true,
	}}})
	open := c.Open()
	if open == nil {
		t.Fatal("no open message")
	}
	if open.From != 3 {
		t.Errorf("From = %d, want 3 — the region starts where our knowledge does", open.From)
	}
	if len(open.Nodes) != 1 || open.Nodes[0].Markdown != "tail" {
		t.Errorf("nodes = %+v, want only the node the part carried", open.Nodes)
	}
	if open.Inquiry != "" {
		t.Errorf("inquiry = %q on a region that does not start the turn", open.Inquiry)
	}
}
