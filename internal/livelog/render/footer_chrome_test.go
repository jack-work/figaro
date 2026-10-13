package render

import (
	"strings"
	"testing"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// A region being frozen trails its role's closer, not the pinned footer, or
// one status bar is stranded in scrollback per message. Open must precede
// Freeze here, as it does in production.
func TestIncipit_FrozenExchangeDoesNotStrandTheFooter(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	reply := []livedoc.Node{{ID: "a0", Type: "prose", Role: livedoc.RoleOutput, Markdown: "pronto"}}
	exchange := aria.Message{Turn: 1, From: 0, Inquiry: "test",
		Role: livedoc.RoleOutput, Nodes: reply}

	in.OpenThinking(livedoc.RoleOutput) // submit: footer only
	in.Open(aria.Message{Turn: 1, From: 0, Inquiry: "test", Role: livedoc.RoleInput})
	in.Open(exchange) // the reply streams in under the question
	in.Freeze(exchange)

	scr := strings.Join(ft.Screen(), "\n")
	if got := strings.Count(scr, "---- aria abcd1234 ---"); got != 1 {
		t.Errorf("status bookend appears %d times, want exactly 1: the frozen "+
			"prompt stranded the pinned footer in scrollback\n---\n%s", got, scr)
	}
	for _, want := range []string{"test", "pronto"} {
		if !strings.Contains(scr, want) {
			t.Errorf("%q missing from scrollback\n---\n%s", want, scr)
		}
	}
}
