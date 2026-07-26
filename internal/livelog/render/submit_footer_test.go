package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// bookend/rule stand-ins so the footer is identifiable in a capture.
func withChrome(in *Incipit) {
	in.Header = func(role string) string {
		switch role {
		case livedoc.RoleInput:
			return "> input"
		case livedoc.RoleOutput:
			return "< figaro"
		}
		return ""
	}
	in.Bookend = func() []string { return []string{"---- aria abcd1234 ---", "FOOTER thinking"} }
	in.Rule = func() string { return "--------" }
}

// The footer is pinned at submit, before the prompt has round-tripped. When the
// prompt then arrives it must land ABOVE the footer, not below it: a status bar
// stranded above the very message it describes is the regression this guards.
func TestIncipit_PromptLandsAboveThePinnedFooter(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	in.OpenThinking(livedoc.RoleOutput) // submit: footer only
	in.Freeze(aria.Message{Turn: 1, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "hello?"}}})

	scr := ft.Screen()
	joined := strings.Join(scr, "\n")
	prompt, footer := -1, -1
	for i, l := range scr {
		if strings.Contains(l, "hello?") && prompt < 0 {
			prompt = i
		}
		if strings.Contains(l, "FOOTER") {
			footer = i // last one wins: the repainted placeholder
		}
	}
	if prompt < 0 {
		t.Fatalf("prompt missing:\n%s", joined)
	}
	if footer < 0 {
		t.Fatalf("footer missing after freeze — it must stay pinned:\n%s", joined)
	}
	if footer < prompt {
		t.Fatalf("footer (row %d) is ABOVE the prompt (row %d):\n%s", footer, prompt, joined)
	}
	if strings.Count(joined, "FOOTER") != 1 {
		t.Fatalf("footer duplicated (%d copies) — the placeholder was not erased:\n%s",
			strings.Count(joined, "FOOTER"), joined)
	}
}

// A viewport too short for header+body+footer shows the footer alone: it is the
// permanent fixture of the view, and thrashing a header into two rows is worse
// than showing nothing else.
func TestIncipit_TinyViewportShowsOnlyTheFooter(t *testing.T) {
	ft := NewFakeTerminal(60, 2)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	in.Open(1, 0, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body text here"}})

	joined := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(joined, "FOOTER") {
		t.Fatalf("footer must survive a tiny viewport:\n%s", joined)
	}
	if strings.Contains(joined, "body text here") {
		t.Fatalf("body must not draw below 3 rows:\n%s", joined)
	}
	if strings.Contains(joined, "< figaro") {
		t.Fatalf("header must not draw below 3 rows:\n%s", joined)
	}
}

// CONTENT LOSS GUARD. Below the pager floor the live region draws the footer
// alone, so the body is never painted — which means Freeze must print the
// message in full instead of assuming its rows are already on screen. Without
// that, the reply vanishes from scrollback entirely: measured at h=2 and h=3,
// where only the first streamed character survived, and at h=4, where a stale
// "T" sat above the real text forever because a row that has scrolled into
// history can never be repainted.
func TestIncipit_TinyViewportKeepsTheReplyInScrollback(t *testing.T) {
	for _, h := range []int{2, 3, 4, 6, 9} {
		ft := NewFakeTerminal(60, h)
		in := NewIncipit(ft, NodeText{})
		withChrome(in)

		in.OpenThinking(livedoc.RoleOutput)
		// stream a partial, then the full text — the exact shape that stranded "T"
		in.Open(7, 0, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "T"}})
		in.Open(7, 0, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "TINYREPLY"}})
		in.Freeze(aria.Message{Turn: 7, Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "TINYREPLY"}}})

		all := strings.Join(ft.Screen(), "\n")
		if n := strings.Count(all, "TINYREPLY"); n != 1 {
			t.Errorf("h=%d: reply appears %d times, want exactly 1:\n%s", h, n, all)
		}
		// the stale partial must not survive as its own row
		for _, l := range ft.Screen() {
			if strings.TrimSpace(l) == "T" {
				t.Errorf("h=%d: orphaned partial frame %q left in scrollback:\n%s", h, "T", all)
				break
			}
		}
	}
}

// A steer splits the agent's run in two, and the leading half is routinely
// invisible — thinking is hidden by default, a tool may already be drawn. A
// header over an empty run showed as a bare "‹ figaro" sitting directly above
// "↳ input": two headers for one steer, the first labelling nothing.
func TestIncipit_NoHeaderOverAnEmptyVoiceRun(t *testing.T) {
	ft := NewFakeTerminal(60, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	// an output run whose only node renders to nothing
	in.Freeze(aria.Message{Turn: 3, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{ID: "n0", Type: livedoc.NodeProse, Markdown: "   "}}})
	// then the steer, which must carry its own header and be the only one
	in.Freeze(aria.Message{Turn: 3, From: 1, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{{ID: "n1", Type: livedoc.NodeSteering, Markdown: "steer me"}}})

	joined := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(joined, "steer me") {
		t.Fatalf("steer text missing:\n%s", joined)
	}
	if n := strings.Count(joined, "< figaro"); n != 0 {
		t.Errorf("empty output run printed %d header(s) over no content:\n%s", n, joined)
	}
	if n := strings.Count(joined, "> input"); n != 1 {
		t.Errorf("steer run should carry exactly one header, got %d:\n%s", n, joined)
	}
}
