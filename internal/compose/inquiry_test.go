package compose

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

func inqUser(cs ...message.Content) message.Message {
	return message.Message{Role: message.RoleInput, Content: cs}
}

func inqResult(id string) message.Content {
	return message.Content{Type: message.ContentToolResult, ToolCallID: id, Text: "ok"}
}

func inqConversation() []message.Message {
	return []message.Message{
		{Role: message.RoleInput},
		inqUser(prose("quick test")),
		asstLT(0, think("hm"), tool("t1", "bash")),
		inqUser(inqResult("t1"), prose("actually, check X")),
		asstLT(0, prose("done")),
		inqUser(prose("next question")),
		asstLT(0, prose("answer")),
	}
}

func TestTurns_EveryTurnHasExactlyOneInquiry(t *testing.T) {
	tns := Turns(inqConversation())
	if len(tns) == 0 {
		t.Fatal("no turns")
	}
	for _, tn := range tns {
		if tn.Inquiry == "" {
			t.Errorf("turn %d has no inquiry, a turn cannot open without one", tn.ID)
		}
	}
	if got := tns[0].Inquiry; got != "quick test" {
		t.Errorf("turn 1 inquiry = %q, want the opening question only (not the steering text)", got)
	}
}

func TestTurns_InquiryIsNotANode(t *testing.T) {
	for _, tn := range Turns(inqConversation()) {
		for i, n := range tn.Nodes {
			if n.Markdown == tn.Inquiry {
				t.Errorf("turn %d node %d echoes the inquiry %q: the question is not a node",
					tn.ID, i, tn.Inquiry)
			}
			if n.Role == livedoc.RoleInput && n.Type != livedoc.NodeSteering {
				t.Errorf("turn %d node %d is %s/input; only steering speaks in the user's voice now",
					tn.ID, i, n.Type)
			}
		}
	}
	tns := Turns(inqConversation())
	if n := tns[0].Nodes[0]; n.Type != livedoc.NodeThinking {
		t.Errorf("turn 1 node 0 = %s, want the agent's first block (thinking)", n.Type)
	}
}
