package uiir

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

const ariaSender = "a5647bfbc216b8a09ffea9586393c289"

func namer(s string) string {
	if s == ariaSender {
		return "nursery-lioness"
	}
	return ""
}

func prompt(lt uint64, steering bool, segs ...message.Content) message.Message {
	return message.Message{Role: message.RoleInput, LogicalTime: lt, Steering: steering, Content: segs}
}

// Every place the projector hands out a sender, it hands out the name with
// it: the opening inquiry, a read page's turns, and a steering node.
func TestTheProjectorNamesEverySender(t *testing.T) {
	p := New(nil).Naming(namer)
	ask := prompt(1, false, message.SenderText(ariaSender, "hello"), message.SenderText("Jack", "hi"))

	segs := p.InquirySegments(ask)
	if len(segs) != 2 || segs[0].Name != "nursery-lioness" || segs[1].Name != "" || segs[0].Sender != ariaSender {
		t.Fatalf("inquiry segments %+v", segs)
	}

	answer := message.Message{Role: message.RoleOutput, LogicalTime: 2, Content: []message.Content{message.TextContent("ok")}}
	steer := prompt(3, true, message.SenderText(ariaSender, "and also"))
	turns := p.Turns([]message.Message{ask, answer, steer})
	if len(turns) == 0 || len(turns[0].InquirySegments) == 0 || turns[0].InquirySegments[0].Name != "nursery-lioness" {
		t.Fatalf("turns %+v", turns)
	}
	steered := false
	for _, n := range turns[0].Nodes {
		if n.Sender == ariaSender {
			steered = true
			if n.SenderName != "nursery-lioness" {
				t.Fatalf("a read page's steering node %+v", n)
			}
		}
	}
	if !steered {
		t.Fatalf("no steering node in %+v", turns[0].Nodes)
	}

	prefix, suffix, _ := p.Nodes([]message.Message{steer}, nil, nil)
	for _, n := range append(prefix, suffix...) {
		if n.Sender == ariaSender && n.SenderName != "nursery-lioness" {
			t.Fatalf("a live steering node %+v", n)
		}
	}
}
