package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/term"
)

// sgrOf is the escape one of term's roles opens with, so a test can ask which
// voice painted a row without stripping the answer away.
func sgrOf(paint func(string) string) string {
	s := paint("\x00")
	return s[:strings.IndexByte(s, 0)]
}

func lineText(ls []FencedLine) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Text
	}
	return out
}

// The fence is punctuation: it declares the region and is not part of it.
func TestFencedLinesSplitsDiffRegionsAndDropsTheFences(t *testing.T) {
	text := "running the patch\n```figdiff\n-old\n+new\n```\ndone"
	got := FencedLines(text)
	if want := []string{"running the patch", "-old", "+new", "done"}; strings.Join(lineText(got), "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q", lineText(got))
	}
	for i, l := range got {
		if want := i == 1 || i == 2; l.Diff != want {
			t.Fatalf("line %d (%q) diff=%v, want %v", i, l.Text, l.Diff, want)
		}
	}
}

// A stream cut inside a block still draws as a diff: the region is open until
// something closes it, and half a diff is still a diff.
func TestFencedLinesUnclosedRegionStaysOpen(t *testing.T) {
	got := FencedLines("```diff\n+added")
	if len(got) != 1 || !got[0].Diff || got[0].Text != "+added" {
		t.Fatalf("lines = %+v", got)
	}
}

// Fences this scanner has no opinion about are left where they are, text and
// all: it is not a markdown parser.
func TestFencedLinesLeavesOtherFencesAlone(t *testing.T) {
	text := "```go\nfunc f() {}\n```"
	got := lineText(FencedLines(text))
	if strings.Join(got, "\n") != text {
		t.Fatalf("lines = %q", got)
	}
	if HasDiff(text) {
		t.Fatal("a go fence is not a diff")
	}
}

// The file headers are read before the +/- lines they start with.
func TestDiffPaintReadsTheMarkers(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	same := func(a, b func(string) string) bool { return a("x") == b("x") }
	for _, tc := range []struct {
		src  string
		want func(string) string
	}{
		{"+added", term.DiffAdd},
		{"-removed", term.DiffDel},
		{"+++ b/x.go", term.Label},
		{"--- a/x.go", term.Label},
		{"@@ -1,3 +1,4 @@", term.Cyan},
		{" context", func(s string) string { return s }},
		{"+3     last = None", term.DiffAdd},
	} {
		if !same(DiffPaint(tc.src), tc.want) {
			t.Fatalf("%q painted %q", tc.src, DiffPaint(tc.src)("x"))
		}
	}
}

// A wrapped continuation keeps its source line's side of the diff.
func TestDiffRowsWrapKeepsTheSide(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	rows := DiffRows("+abcdefghij", 5)
	if len(rows) != 3 {
		t.Fatalf("rows = %q", rows)
	}
	for _, r := range rows {
		if r != term.DiffAdd(StripEscapes(r)) {
			t.Fatalf("row %q lost the add colour", r)
		}
	}
}

// Prose draws a fenced diff itself, in figaro's colours, rather than handing
// it to the highlighter.
func TestProseDrawsFencedDiffs(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	rows := Prose("before\n\n```diff\n-old line\n+new line\n```\n\nafter", 60)
	addSGR, delSGR := sgrOf(term.DiffAdd), sgrOf(term.DiffDel)
	var del, add bool
	for _, r := range rows {
		switch strings.TrimSpace(StripEscapes(r)) {
		case "-old line":
			del = strings.Contains(r, delSGR)
		case "+new line":
			add = strings.Contains(r, addSGR)
		}
	}
	if !del || !add {
		t.Fatalf("fenced diff was not painted (del=%v add=%v):\n%q", del, add, rows)
	}
	if strings.Contains(strings.Join(rows, "\n"), "```") {
		t.Fatalf("the fence reached the screen:\n%q", rows)
	}
}
