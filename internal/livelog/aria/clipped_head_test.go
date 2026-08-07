package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// A catch-up read of a turn TOO BIG FOR ONE PAGE comes back clipped at the
// head: the part starts at node From>0 and — correctly — carries no inquiry,
// because the slice that STARTS the turn is the only one that may.
//
// The client used to promote such a part as a message starting at node ZERO.
// absorb pads the slots below From so the ids stay positional, and the seal
// path then handed those padding slots out as a HEAD SLICE. A head slice is
// where every renderer draws the question — and this one had none. That is the
// whole of the user's report: "the inquiry doesn't always appear, even past
// inquiries", where the something-he-could-not-put-his-finger-on was the page
// budget, i.e. how big the turn happened to be.
//
// What the reader is owed instead is the head itself, and it arrives by paging
// up: see TestScrollingBackCompletesATurnsHead.
func TestClient_ClippedPartDoesNotFabricateAHeadSlice(t *testing.T) {
	c := NewClient()
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "the tail of a long turn"}}
	c.Apply(Page{Parts: []TurnPart{{
		Turn:        Turn{ID: 3, Inquiry: "the question", Sealed: true, Nodes: nodes},
		From:        7,
		ClippedHead: true,
	}}})

	v := c.View()
	if len(v.Closed) != 1 {
		t.Fatalf("closed = %d messages, want 1", len(v.Closed))
	}
	m := v.Closed[0]
	if m.From != 7 {
		t.Errorf("From = %d, want 7: a clipped part must not claim to start the turn", m.From)
	}
	if len(m.Nodes) != 1 || m.Nodes[0].Markdown != nodes[0].Markdown {
		t.Errorf("nodes = %+v, want just the clipped part's own node", m.Nodes)
	}
	if m.Inquiry != "" {
		t.Errorf("inquiry = %q on a clipped slice: it would print the question mid-turn", m.Inquiry)
	}
}

func TestClient_HeadPartCarriesTheInquiry(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 4, Inquiry: "the question", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "an answer"}}},
		From: 0,
	}}})
	v := c.View()
	if len(v.Closed) != 1 || v.Closed[0].Inquiry != "the question" {
		t.Fatalf("closed = %+v, want one message carrying the question", v.Closed)
	}
}

// A clipped catch-up read of the turn STILL STREAMING must not put the padding
// slots into the live region either: the open message is the part of the turn
// we actually hold, and it does not start the turn, so it draws no question.
func TestClient_ClippedCatchUpKeepsTheOpenRegionHonest(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 5, Inquiry: "the question",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "tail"}},
			Live:  &Live{From: 9, V: 3}},
		From:        9,
		ClippedHead: true,
	}}})

	open := c.View().Open
	if open == nil {
		t.Fatal("no open message")
	}
	if open.From != 9 {
		t.Errorf("From = %d, want 9", open.From)
	}
	if len(open.Nodes) != 1 || open.Nodes[0].Markdown != "tail" {
		t.Errorf("nodes = %+v, want only the node the part carried", open.Nodes)
	}
	if open.Inquiry != "" {
		t.Errorf("inquiry = %q on a region that does not start the turn", open.Inquiry)
	}
}

// THE LIVE SUFFIX OF A LONG TURN MUST NOT RE-ASK THE TURN'S OWN QUESTION.
//
// Once a streaming turn's head is released to scrollback the open region sits
// at From>0. A rule that let any slice but the head speak therefore drew
// "> input" and the whole question again, mid-answer, immediately under the
// output above it and with no rule between them (the separator is a TURN
// boundary, and this is the same turn). Reported from a live session: a test
// run printed FAIL and the question reappeared on the next row.
func TestClient_LiveSuffixDoesNotRepeatTheQuestion(t *testing.T) {
	c := NewClient()
	c.SetClosedLimit(50)
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Inquiry: "the question", Live: &Live{From: 0, V: 1},
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "first"}}},
		From: 0,
	}}})
	// The head closes and reaches scrollback; the turn keeps streaming.
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Live: &Live{From: 1, V: 2},
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: "first"},
				{Type: livedoc.NodeProse, Markdown: "second"},
			}},
		From: 0,
	}}})

	v := c.View()
	if len(v.Closed) != 1 || v.Closed[0].Inquiry != "the question" {
		t.Fatalf("the head slice must carry the question, got %+v", v.Closed)
	}
	if v.Open == nil {
		t.Fatal("the turn is still streaming; there should be an open region")
	}
	if v.Open.From == 0 {
		t.Fatalf("fixture: the head was not released, so nothing is being tested")
	}
	if v.Open.Inquiry != "" {
		t.Errorf("the live suffix at offset %d re-draws the question %q",
			v.Open.From, v.Open.Inquiry)
	}
}
