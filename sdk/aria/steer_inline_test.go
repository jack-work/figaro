package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A steer is an inline annotation, not a voice change: a turn folds into one message.
func TestClient_SteerDoesNotSplitTheTurnsRun(t *testing.T) {
	nodes := []livedoc.Node{
		{Type: livedoc.NodeThinking, Markdown: "thinking"},
		{Type: livedoc.NodeTool, Name: "bash"},
		{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "test"},
		{Type: livedoc.NodeTool, Name: "bash"},
		{Type: livedoc.NodeProse, Markdown: "Ecco fatto!"},
	}

	c := NewClient()
	var got []Message
	c.OnClosed = func(m Message) { got = append(got, m) }
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{
		ID: 1, Inquiry: "do one, then sleep", Sealed: true, Nodes: nodes,
	}}}}, Notify)

	if len(got) != 1 {
		t.Fatalf("turn folded into %d messages, want 1: the steer split the agent's run, "+
			"which is what gives it a header and a pair of rules it must not have", len(got))
	}
	m := got[0]
	if m.From != 0 || len(m.Nodes) != len(nodes) {
		t.Fatalf("message = {From:%d, %d nodes}, want {From:0, %d nodes}", m.From, len(m.Nodes), len(nodes))
	}
	if m.Role != livedoc.RoleOutput {
		t.Errorf("run voice = %q, want %q: the nodes are all the agent's", m.Role, livedoc.RoleOutput)
	}
	if m.Inquiry != "do one, then sleep" {
		t.Errorf("inquiry = %q, want it carried by the turn's first slice", m.Inquiry)
	}
}

// A turn that produced nothing still closes one message.
func TestClient_InquiryOnlyTurnStillCloses(t *testing.T) {
	c := NewClient()
	var got []Message
	c.OnClosed = func(m Message) { got = append(got, m) }
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Inquiry: "hello?"}}}}, Notify)
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Inquiry: "hello?", Sealed: true}}}}, Notify)

	if len(got) != 1 {
		t.Fatalf("got %d closed messages, want 1", len(got))
	}
	if got[0].Inquiry != "hello?" || len(got[0].Nodes) != 0 {
		t.Fatalf("message = %+v, want the bare inquiry", got[0])
	}
	if got[0].Role != livedoc.RoleInput {
		t.Errorf("role = %q, want %q: nobody but the user spoke", got[0].Role, livedoc.RoleInput)
	}
}
