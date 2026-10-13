package render

import (
	"strings"
	"testing"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// The live region paints the question at submit and keeps it through every
// repaint until the exchange freezes, once.
func TestIncipit_InquiryPaintsAtSubmitAndFreezesOnce(t *testing.T) {
	ft := NewFakeTerminal(60, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	// The agent's header must not appear over an empty body.
	in.OpenThinking(livedoc.RoleOutput)
	in.Open(aria.Message{Turn: 1, Inquiry: "WHATSTHEWORD", Role: livedoc.RoleInput})
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "WHATSTHEWORD") {
		t.Fatalf("the question must paint the instant it is asked:\n%s", scr)
	}

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

// A later slice of the same turn carries no inquiry.
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
		t.Fatalf("the question appears %d times, want 1: only the first slice carries it:\n%s", got, scr)
	}
}
