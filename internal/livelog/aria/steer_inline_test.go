package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// A steer is an INLINE ANNOTATION, not a voice change.
//
// The user reported: "steering turns seems to wind up double printed, and they
// all get entire rows." A steering node carries Role input, so the fold used to
// see a voice change there, close the agent's run and open an input run around
// the steer — giving it its own "❯ input" header AND its own pair of full-width
// rules, and cutting the agent's output into two blocks.
//
// There is no voice split left to get wrong: the inquiry is text on the turn,
// so every node in a turn is agent output and a turn folds into ONE message.
// This pins that at the fold, which is what every surface renders from.
func TestClient_SteerDoesNotSplitTheTurnsRun(t *testing.T) {
	// The canonical shape from the user's own reference (/tmp/test): one agent
	// run containing a tool, a steer, another tool and the closing prose,
	// under a turn whose inquiry is text.
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
	}}}})

	if len(got) != 1 {
		t.Fatalf("turn folded into %d messages, want 1 — the steer split the agent's run, "+
			"which is what gives it a header and a pair of rules it must not have", len(got))
	}
	m := got[0]
	if m.From != 0 || len(m.Nodes) != len(nodes) {
		t.Fatalf("message = {From:%d, %d nodes}, want {From:0, %d nodes}", m.From, len(m.Nodes), len(nodes))
	}
	if m.Role != livedoc.RoleOutput {
		t.Errorf("run voice = %q, want %q — the nodes are all the agent's", m.Role, livedoc.RoleOutput)
	}
	if m.Inquiry != "do one, then sleep" {
		t.Errorf("inquiry = %q, want it carried by the turn's first slice", m.Inquiry)
	}
}

// A turn that produced nothing — interrupted before its first block — still
// closes one message, so the question the user asked reaches scrollback.
func TestClient_InquiryOnlyTurnStillCloses(t *testing.T) {
	c := NewClient()
	var got []Message
	c.OnClosed = func(m Message) { got = append(got, m) }
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Inquiry: "hello?"}}}})
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Inquiry: "hello?", Sealed: true}}}})

	if len(got) != 1 {
		t.Fatalf("got %d closed messages, want 1", len(got))
	}
	if got[0].Inquiry != "hello?" || len(got[0].Nodes) != 0 {
		t.Fatalf("message = %+v, want the bare inquiry", got[0])
	}
	if got[0].Role != livedoc.RoleInput {
		t.Errorf("role = %q, want %q — nobody but the user spoke", got[0].Role, livedoc.RoleInput)
	}
}
