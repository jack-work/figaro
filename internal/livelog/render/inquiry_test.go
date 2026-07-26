package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// THE QUESTION IS TEXT ON THE TURN, and the inline renderer draws it from
// there. It used to be Nodes[0], which is why nothing here had to know about
// it; now the live region has to paint it itself, at submit — before a single
// token of the reply exists — and keep it through every repaint until the
// exchange freezes, ONCE.
func TestIncipit_InquiryPaintsAtSubmitAndFreezesOnce(t *testing.T) {
	ft := NewFakeTerminal(60, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	// Submit: the question is all there is. The agent's header must not appear
	// over an empty body.
	in.OpenThinking(livedoc.RoleOutput)
	in.Open(aria.Message{Turn: 1, Inquiry: "WHATSTHEWORD", Role: livedoc.RoleInput})
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "WHATSTHEWORD") {
		t.Fatalf("the question must paint the instant it is asked:\n%s", scr)
	}

	// The reply streams into the same region, then a tick repaints it: the
	// question is region state, so a repaint that forgot it would drop it.
	reply := []livedoc.Node{{ID: "a0", Type: "prose", Markdown: "REDRUM"}}
	exchange := aria.Message{Turn: 1, Inquiry: "WHATSTHEWORD", Role: livedoc.RoleOutput, Nodes: reply}
	in.Open(exchange)
	in.Tick(reply)
	in.Freeze(exchange)

	scr := strings.Join(ft.Screen(), "\n")
	if got := strings.Count(scr, "WHATSTHEWORD"); got != 1 {
		t.Fatalf("the question appears %d times in scrollback, want 1:\n%s", got, scr)
	}
	if got := strings.Count(scr, "REDRUM"); got != 1 {
		t.Fatalf("the reply appears %d times, want 1:\n%s", got, scr)
	}
	iq, fig := strings.Index(scr, "> input"), strings.Index(scr, "< figaro")
	if iq < 0 || fig < 0 || iq > fig {
		t.Fatalf("want the input header above the agent's:\n%s", scr)
	}
}

// A LATER slice of the same turn carries no inquiry (only the first does), so
// the question must not reappear above the second half of a steered reply.
func TestIncipit_LaterSliceDoesNotRepeatTheQuestion(t *testing.T) {
	ft := NewFakeTerminal(60, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	head := []livedoc.Node{{ID: "a0", Type: "prose", Markdown: "FIRSTHALF"}}
	tail := []livedoc.Node{{ID: "a1", Type: "prose", Markdown: "SECONDHALF"}}
	in.Open(aria.Message{Turn: 1, Inquiry: "ASKED", Role: livedoc.RoleOutput, Nodes: head})
	in.Freeze(aria.Message{Turn: 1, Inquiry: "ASKED", Role: livedoc.RoleOutput, Nodes: head})
	in.Open(aria.Message{Turn: 1, From: 1, Role: livedoc.RoleOutput, Nodes: tail})
	in.Freeze(aria.Message{Turn: 1, From: 1, Role: livedoc.RoleOutput, Nodes: tail})

	scr := strings.Join(ft.Screen(), "\n")
	if got := strings.Count(scr, "ASKED"); got != 1 {
		t.Fatalf("the question appears %d times, want 1 — only the first slice carries it:\n%s", got, scr)
	}
}
