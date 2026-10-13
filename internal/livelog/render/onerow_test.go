package render

import (
	"strings"
	"testing"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// At h=1 the two-row footer-only region would overflow the viewport and
// scroll the partial into history.
func TestIncipit_SingleRowViewportKeepsTheReply(t *testing.T) {
	const full = "ONEROW"
	for _, h := range []int{1, 2, 3, 4, 5, 24} {
		ft := NewFakeTerminal(40, h)
		in := NewIncipit(ft, NodeText{})
		withChrome(in)

		in.OpenThinking(livedoc.RoleOutput)
		in.Open(aria.Message{Turn: 9, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "ONE"}}})
		in.Open(aria.Message{Turn: 9, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: full}}})
		in.Freeze(aria.Message{Turn: 9, From: 0, Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: full}}})

		all := strings.Join(ft.Screen(), "\n")
		if n := strings.Count(all, full); n != 1 {
			t.Errorf("h=%d: reply appears %d times, want exactly 1:\n%s", h, n, all)
		}
		for _, l := range ft.Screen() {
			if strings.TrimSpace(l) == "ONE" {
				t.Errorf("h=%d: orphaned partial %q left in scrollback:\n%s", h, "ONE", all)
				break
			}
		}
	}
}

// The suppressed region must never be taller than the terminal, and what
// survives clipping must be the status line.
func TestIncipit_SuppressedRegionFitsTheViewport(t *testing.T) {
	for _, h := range []int{1, 2, 3} {
		ft := NewFakeTerminal(40, h)
		in := NewIncipit(ft, NodeText{})
		in.Bookend = func() []string { return []string{"RULEROW", "STATUSROW"} }

		in.Open(aria.Message{Turn: 4, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body"}}})

		rows := in.compose([]livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body"}})
		if len(rows) > h {
			t.Errorf("h=%d: suppressed region is %d rows, taller than the viewport: %q", h, len(rows), rows)
		}
		if h >= 1 && len(rows) > 0 && rows[len(rows)-1] != "STATUSROW" {
			t.Errorf("h=%d: last row = %q, want the status line to survive clipping", h, rows[len(rows)-1])
		}
	}
}
