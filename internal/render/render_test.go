package render

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func visible(lines []string) string { return stripANSI(strings.Join(lines, "\n")) }

func TestProse_Paragraph(t *testing.T) {
	out := visible(Prose("Hello, **world**.", 80))
	if !strings.Contains(out, "Hello, world.") {
		t.Fatalf("paragraph text missing; got %q", out)
	}
}

func TestProse_Table(t *testing.T) {
	md := "| col | val |\n| --- | --- |\n| a | 1 |\n| b | 2 |\n"
	out := visible(Prose(md, 80))
	for _, want := range []string{"col", "val", "a", "b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table cell %q missing; got:\n%s", want, out)
		}
	}
}

func TestProse_Deterministic(t *testing.T) {
	md := "# Title\n\nSome *prose* and a list:\n\n- a\n- b\n\n```go\nx := 1\n```\n"
	if strings.Join(Prose(md, 72), "\n") != strings.Join(Prose(md, 72), "\n") {
		t.Fatal("Prose is not deterministic for identical inputs")
	}
}

func TestProse_Empty(t *testing.T) {
	if rows := Prose("", 80); len(rows) != 0 {
		t.Fatalf("empty markdown should render no rows, got %d", len(rows))
	}
}

// Prose renders a code snippet as a glamour code block: indented (deeper
// than the 2-col prose margin) and set off by blank lines — not the col-0
// clamped tool-output form.
func TestProse_CodeBlockIndentedAndSpaced(t *testing.T) {
	rows := Prose("Run this:\n\n```sh\nfind . -name '*.tf'\n```\n\nDone.", 80)
	codeIdx := -1
	for i, r := range rows {
		if strings.Contains(stripANSI(r), "find . -name") {
			codeIdx = i
		}
	}
	if codeIdx < 0 {
		t.Fatalf("code content missing:\n%s", visible(rows))
	}
	// The code row is indented past the prose's 2-col margin.
	code := stripANSI(rows[codeIdx])
	if n := len(code) - len(strings.TrimLeft(code, " ")); n < 3 {
		t.Errorf("code block should be indented (got %d leading spaces): %q", n, code)
	}
	// A blank line separates the prose from the code block.
	if codeIdx == 0 || strings.TrimSpace(stripANSI(rows[codeIdx-1])) != "" {
		t.Errorf("expected a blank line before the code block; row above: %q", stripANSI(rows[codeIdx-1]))
	}
}

// A mid-stream unclosed fence is synth-closed so Prose still renders a code
// block (stable structure as it streams).
func TestProse_UnclosedFenceSynthClosed(t *testing.T) {
	rows := Prose("Here:\n\n```sh\nfind . -name", 80) // fence open
	if !strings.Contains(visible(rows), "find . -name") {
		t.Fatalf("streaming (unclosed) code fence should still render its content:\n%s", visible(rows))
	}
}
