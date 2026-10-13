package render

import (
	"strings"
	"testing"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

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

// The footer is pinned at submit; the prompt must land above it when it arrives.
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
		t.Fatalf("footer missing after freeze: it must stay pinned:\n%s", joined)
	}
	if footer < prompt {
		t.Fatalf("footer (row %d) is ABOVE the prompt (row %d):\n%s", footer, prompt, joined)
	}
	if strings.Count(joined, "FOOTER") != 1 {
		t.Fatalf("footer duplicated (%d copies): the placeholder was not erased:\n%s",
			strings.Count(joined, "FOOTER"), joined)
	}
}

// A viewport too short for header+body+footer shows the footer alone.
func TestIncipit_TinyViewportShowsOnlyTheFooter(t *testing.T) {
	ft := NewFakeTerminal(60, 2)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	in.Open(aria.Message{Turn: 1, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body text here"}}})

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

// Below the pager floor the body is never painted, so Freeze must print the
// message in full rather than assume its rows are on screen.
func TestIncipit_TinyViewportKeepsTheReplyInScrollback(t *testing.T) {
	for _, h := range []int{2, 3, 4, 6, 9} {
		ft := NewFakeTerminal(60, h)
		in := NewIncipit(ft, NodeText{})
		withChrome(in)

		in.OpenThinking(livedoc.RoleOutput)
		in.Open(aria.Message{Turn: 7, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "T"}}})
		in.Open(aria.Message{Turn: 7, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "TINYREPLY"}}})
		in.Freeze(aria.Message{Turn: 7, Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "TINYREPLY"}}})

		all := strings.Join(ft.Screen(), "\n")
		if n := strings.Count(all, "TINYREPLY"); n != 1 {
			t.Errorf("h=%d: reply appears %d times, want exactly 1:\n%s", h, n, all)
		}
		for _, l := range ft.Screen() {
			if strings.TrimSpace(l) == "T" {
				t.Errorf("h=%d: orphaned partial frame %q left in scrollback:\n%s", h, "T", all)
				break
			}
		}
	}
}

// A slice whose nodes all render to nothing must not draw a bare header.
func TestIncipit_NoHeaderOverAnEmptyRun(t *testing.T) {
	ft := NewFakeTerminal(60, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	in.Freeze(aria.Message{Turn: 3, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{ID: "n0", Type: livedoc.NodeProse, Markdown: "   "}}})
	// A steer rides inside the agent's run and earns no header of its own.
	in.Freeze(aria.Message{Turn: 3, From: 1, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{ID: "n1", Type: livedoc.NodeSteering, Markdown: "steer me"}}})

	joined := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(joined, "steer me") {
		t.Fatalf("steer text missing:\n%s", joined)
	}
	if n := strings.Count(joined, "< figaro"); n != 0 {
		t.Errorf("no header: the empty run has nothing to head and a steer is not a question; got %d:\n%s", n, joined)
	}
	if n := strings.Count(joined, "> input"); n != 0 {
		t.Errorf("a steer is not a voice change; got %d input headers:\n%s", n, joined)
	}
}
