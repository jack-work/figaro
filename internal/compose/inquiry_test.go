package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// inqUser is a plain input message — the shape that opens a turn.
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
		inqUser(inqResult("t1"), prose("actually, check X")), // turn 1 — steering, not a new turn
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
	tns := Turns(inqConversation(), nil, nil)
	if len(tns) == 0 {
		t.Fatal("no turns")
	}
	for _, tn := range tns {
		if tn.Inquiry == "" {
			t.Errorf("turn %d has no inquiry — a turn cannot open without one", tn.ID)
		}
	}
	if got := tns[0].Inquiry; got != "quick test" {
		t.Errorf("turn 1 inquiry = %q, want the opening question only (not the steering text)", got)
	}
}

// The inquiry is the opening question as TEXT, and it agrees with the prompt
// node the projection still emits. When that node is removed (the S32 seam)
// this test becomes the proof that nothing was lost.
func TestTurns_InquiryAgreesWithThePromptNode(t *testing.T) {
	for _, tn := range Turns(inqConversation(), nil, nil) {
		if len(tn.Nodes) == 0 {
			t.Fatalf("turn %d has no nodes", tn.ID)
		}
		n := tn.Nodes[0]
		if n.Type != livedoc.NodeProse || n.Role != livedoc.RoleInput {
			t.Fatalf("turn %d node 0 = %s/%s, want prose/input", tn.ID, n.Type, n.Role)
		}
		if n.Markdown != tn.Inquiry {
			t.Errorf("turn %d: inquiry %q != prompt node %q", tn.ID, tn.Inquiry, n.Markdown)
		}
	}
}
