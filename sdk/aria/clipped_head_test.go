package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A part clipped at the head starts at From>0 and carries no inquiry; the
// padding slots below From must never be handed out as a head slice.
func TestClient_ClippedPartDoesNotFabricateAHeadSlice(t *testing.T) {
	c := NewClient()
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "the tail of a long turn"}}
	c.Apply(Page{Parts: []TurnPart{{
		Turn:        Turn{ID: 3, Inquiry: "the question", Sealed: true, Nodes: nodes},
		From:        7,
		ClippedHead: true,
	}}}, Notify)

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
	}}}, Notify)
	v := c.View()
	if len(v.Closed) != 1 || v.Closed[0].Inquiry != "the question" {
		t.Fatalf("closed = %+v, want one message carrying the question", v.Closed)
	}
}

// The same holds for the live region of a turn still streaming.
func TestClient_ClippedCatchUpKeepsTheOpenRegionHonest(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 5, Inquiry: "the question",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "tail"}},
			Live:  &Live{From: 9, V: 3}},
		From:        9,
		ClippedHead: true,
	}}}, Notify)

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

// Once a streaming turn's head is released, the open region sits at From>0
// and must not carry the question again.
func TestClient_LiveSuffixDoesNotRepeatTheQuestion(t *testing.T) {
	c := NewClient()
	c.SetClosedLimit(50)
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Inquiry: "the question", Live: &Live{From: 0, V: 1},
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "first"}}},
		From: 0,
	}}}, Notify)
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Live: &Live{From: 1, V: 2},
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: "first"},
				{Type: livedoc.NodeProse, Markdown: "second"},
			}},
		From: 0,
	}}}, Notify)

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
