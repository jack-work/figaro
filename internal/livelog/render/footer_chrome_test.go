package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// THE FOOTER IS CHROME, NOT CONTENT.
//
// The live region always trails the pinned status footer, so it is visible from
// the instant of submit. But a region being FROZEN must trail its ROLE'S CLOSER
// instead, a plain rule for input, the bookend for output. Freezing the footer
// verbatim stranded one copy in scrollback per message, so a single exchange
// showed TWO status bars:
//
//	> input
//	  test
//	──── aria abc ───        <- stranded: the prompt's own live region carried it
//	test · thinking ⠙ …
//	< figaro
//	  …reply…
//	──── aria abc ───        <- the real one
//	test · completed ✓ …
//
// The user hit this on the FIRST COMMAND he ran, and no unit test caught it,
// because every existing one called Freeze WITHOUT a preceding Open. Production
// never does that: the exchange arrives as a LIVE region (Open) and is then
// sealed (Freeze), which is the only path on which the footer can be committed.
// The real render-call sequence, traced from a live binary:
//
//	OPENTHINKING role=output
//	OPEN   turn=1 from=0 inquiry="test"  <- the question paints at submit
//	OPEN   turn=1 from=0 role=output     <- the reply streams in under it
//	FREEZE turn=1 from=0 role=output     <- dropBelow commits [exchange + FOOTER]
//
// Drive Open before Freeze or this test proves nothing.
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
