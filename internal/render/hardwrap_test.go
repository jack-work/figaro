package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// glamour leaves a token longer than the wrap width whole, and an unclosed fence ignores the width.
func TestOverlongContentIsWrappedNotLost(t *testing.T) {
	cases := map[string]string{
		"url":            "see https://example.com/a/very/long/path/that/will/not/break/anywhere/at/all here",
		"long-token":     "prefix longIdentifierNameThatCannotBeBrokenAcrossLinesAtAllNoMatterWhat suffix",
		"unclosed-fence": "```\nunclosed fence with a long line of content inside it that runs on\n",
		"fence":          "```go\nfunc f() { return errors.New(\"a fairly long line of code here\") }\n```",
		"cjk":            "これは日本語のテキストで、折り返しの計算を確かめるためのものです。さらに続きます。",
	}
	for name, md := range cases {
		for w := 20; w <= 120; w++ {
			rows := Prose(md, w)
			for i, r := range rows {
				if got := cells(r); got > w {
					t.Fatalf("%s w=%d: row %d is %d cells: %q", name, w, i, got, stripSGR(r))
				}
			}
			var got strings.Builder
			for _, r := range rows {
				got.WriteString(strings.TrimSpace(stripSGR(r)))
			}
			for _, want := range longRuns(md) {
				if !strings.Contains(strings.ReplaceAll(got.String(), " ", ""), want) {
					t.Fatalf("%s w=%d: lost %q from the output", name, w, want)
				}
			}
		}
	}
}

func longRuns(md string) []string {
	var out []string
	for _, f := range strings.Fields(md) {
		f = strings.Trim(f, "`")
		if len([]rune(f)) > 24 {
			out = append(out, f)
		}
	}
	return out
}

// A bare ESC before a multi-byte rune must step a whole rune, or the row comes back as invalid UTF-8.
func TestSplitToWidthKeepsRunesWhole(t *testing.T) {
	for _, in := range []string{"\x1bر", "\x1b(Bر", "\x1bOPر", "a\x1b[?25lب", "\x1b_x\x1b\\ن"} {
		for w := 1; w <= 8; w++ {
			for _, row := range splitToWidth(in, w) {
				if !utf8.ValidString(row) {
					t.Fatalf("splitToWidth(%q, %d) produced invalid UTF-8: %q", in, w, row)
				}
			}
		}
	}
}
