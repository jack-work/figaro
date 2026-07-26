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
	in.Freeze(aria.Message{LT: 1, Role: livedoc.RoleInput,
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

	in.Open(1, livedoc.RoleOutput, []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "body text here"}})

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
