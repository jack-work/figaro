package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// At h=1 there is exactly one row. The suppressed (footer-only) region is still
// two rows — rule + status — so painting it overflows the viewport, scrolls the
// partial into history, and the completed reply never lands.
//
// This height only became reachable once term.Size stopped fabricating 80x24
// for terminals it considered too small; before that, h<=2 was reported as 24
// and this row was invisible.
func TestIncipit_SingleRowViewportKeepsTheReply(t *testing.T) {
	const full = "ONEROW"
	for _, h := range []int{1, 2, 3, 4, 5, 24} {
		ft := NewFakeTerminal(40, h)
		in := NewIncipit(ft, NodeText{})
		withChrome(in)

		in.OpenThinking(livedoc.RoleOutput)
		in.Open(9, 0, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "ONE"}})
		in.Open(9, 0, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: full}})
		in.Freeze(aria.Message{LT: 9, From: 0, Role: livedoc.RoleOutput,
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

// The suppressed region must never be taller than the terminal. Whatever
// survives clipping must be the STATUS line — the row the user actually needs —
// not the rule above it.
func TestIncipit_SuppressedRegionFitsTheViewport(t *testing.T) {
	for _, h := range []int{1, 2, 3} {
		ft := NewFakeTerminal(40, h)
		in := NewIncipit(ft, NodeText{})
		in.Bookend = func() []string { return []string{"RULEROW", "STATUSROW"} }

		in.Open(4, 0, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body"}})

		rows := in.compose([]livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body"}})
		if len(rows) > h {
			t.Errorf("h=%d: suppressed region is %d rows, taller than the viewport: %q", h, len(rows), rows)
		}
		if h >= 1 && len(rows) > 0 && rows[len(rows)-1] != "STATUSROW" {
			t.Errorf("h=%d: last row = %q, want the status line to survive clipping", h, rows[len(rows)-1])
		}
	}
}
