package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A TURN MET BY SCROLLING UP KEEPS ITS HEAD: its opening nodes and its
// question: even though the page carrying the tail arrived first.
//
// Reported as "opening old arias omits inquiries entirely". It was never only
// the question: a turn first seen by its TAIL was marked closed, and every
// later part for it was skipped whole, so the opening nodes were dropped in
// silence too. Only the tail of each page-boundary turn survived, and the
// question was then unreachable for good: no amount of scrolling could bring
// back a head the client refused to adopt.
func TestScrollingBackCompletesATurnsHead(t *testing.T) {
	c := NewClient()
	c.SetClosedLimit(50)

	// A backward page delivers the TAIL of an old turn: from > 0, clipped head.
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 5, Inquiry: "WHATDIDIASK", Sealed: true,
			InquirySegments: []InquirySegment{{Sender: "aria 123456", Text: "WHATDIDIASK"}},
			Nodes:           []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THIRD"}}},
		From: 2, ClippedHead: true,
	}}})

	// Scrolling further up delivers the head of that same turn.
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 5, Inquiry: "WHATDIDIASK", Sealed: true,
			InquirySegments: []InquirySegment{{Sender: "aria 123456", Text: "WHATDIDIASK"}},
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: "FIRST"},
				{Type: livedoc.NodeProse, Markdown: "SECOND"},
			}},
		From: 0,
	}}})

	var text []string
	var questions int
	var sender string
	for _, m := range c.View().Closed {
		if m.Turn != 5 {
			continue
		}
		for _, n := range m.Nodes {
			text = append(text, n.Markdown)
		}
		if m.Inquiry != "" {
			questions++
			if len(m.InquirySegments) > 0 {
				sender = m.InquirySegments[0].Sender
			}
			if m.From != 0 {
				t.Errorf("the question belongs to the slice that starts the turn; got from=%d", m.From)
			}
		}
	}
	if want := []string{"FIRST", "SECOND", "THIRD"}; !equalStrings(text, want) {
		t.Errorf("the turn lost content: got %v, want %v", text, want)
	}
	if questions != 1 {
		t.Errorf("the question must be drawn exactly once, got %d", questions)
	}
	if sender != "aria 123456" {
		t.Errorf("who asked must travel with it, got %q", sender)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The half of the old guard that still earns its place, and stays.
func TestHistoryStillDoesNotClaimTheOpenSlot(t *testing.T) {
	c := NewClient()
	c.SetClosedLimit(50)
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 9, Inquiry: "the live question",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "still answering"}},
			Live:  &Live{From: 0, V: 0}},
		From: 0,
	}}})
	if c.View().Open == nil {
		t.Fatal("fixture: turn 9 should be open")
	}
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 4, Inquiry: "an older question", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "old tail"}}},
		From: 2, ClippedHead: true,
	}}})
	open := c.View().Open
	if open == nil {
		t.Fatal("history destroyed the live turn")
	}
	if open.Turn != 9 {
		t.Errorf("the open turn is now %d, a clipped-head part claimed the slot", open.Turn)
	}
	if open.Inquiry != "the live question" {
		t.Errorf("the live turn lost its question: %q", open.Inquiry)
	}
}
