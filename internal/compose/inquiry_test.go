package compose

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

// inqUser is a plain input message: the shape that opens a turn.
func inqUser(cs ...message.Content) message.Message {
	return message.Message{Role: message.RoleInput, Content: cs}
}

// inqResult is a tool_result block, which rides INSIDE a turn.
func inqResult(id string) message.Content {
	return message.Content{Type: message.ContentToolResult, ToolCallID: id, Text: "ok"}
}

// inqConversation is the canonical shape, including the case that would fool a
// naive rule: a steering interjection is an input message riding on a
// tool_result, and it must neither open a turn nor become an inquiry.
func inqConversation() []message.Message {
	return []message.Message{
		{Role: message.RoleInput},                            // boot / state-only: no turn
		inqUser(prose("quick test")),                         // opens turn 1
		asstLT(0, think("hm"), tool("t1", "bash")),           // turn 1
		inqUser(inqResult("t1"), prose("actually, check X")), // turn 1: steering, not a new turn
		asstLT(0, prose("done")),                             // turn 1
		inqUser(prose("next question")),                      // opens turn 2
		asstLT(0, prose("answer")),                           // turn 2
	}
}

// An inquiry is 1:1 with a turn boundary. The user believed this was already
// true; it is, and this pins it so it stays true. turns.Opens already demands
// non-empty prose, so a turn cannot open without an inquiry, and a second
// inquiry cannot arrive without closing the first.
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

// The inquiry is TEXT ON THE TURN and nothing else. It is not node 0, it is not
// any node: a turn's list holds what the AGENT produced plus the steering that
// rode along, so a renderer can tell the question from the answer without
// inspecting a role. The predecessor of this test proved the inquiry text and
// the prompt node agreed: its whole purpose was to license removing the node,
// which this now pins.
func TestTurns_InquiryIsNotANode(t *testing.T) {
	for _, tn := range Turns(inqConversation()) {
		for i, n := range tn.Nodes {
			if n.Markdown == tn.Inquiry {
				t.Errorf("turn %d node %d echoes the inquiry %q: the question is not a node",
					tn.ID, i, tn.Inquiry)
			}
			// Steering is the only input-voice node left.
			if n.Role == livedoc.RoleInput && n.Type != livedoc.NodeSteering {
				t.Errorf("turn %d node %d is %s/input; only steering speaks in the user's voice now",
					tn.ID, i, n.Type)
			}
		}
	}
	// Turn 1 opens with a question and answers it: its first node is the
	// agent's, not the user's.
	tns := Turns(inqConversation())
	if n := tns[0].Nodes[0]; n.Type != livedoc.NodeThinking {
		t.Errorf("turn 1 node 0 = %s, want the agent's first block (thinking)", n.Type)
	}
}
