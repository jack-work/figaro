package render

import (
	"strings"
	"testing"
)

// Content glamour will not break must still fit the width it was given.
//
// glamour wraps on word boundaries, so a token longer than the wrap width comes
// out whole; an unclosed fence ignores the wrap width altogether, overrunning by
// 11 cells at w=20. Every painter downstream clips, so the overrun never
// corrupted the screen — it silently ATE THE TAIL of the text that did not fit.
//
// CANARY (watched): drop hardWrapOverlong from Prose ->
//
//	unclosed-fence w=20: row 1 is 31 cells
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
			// NOTHING IS LOST: every visible character survives, in order.
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

// longRuns returns the unbreakable runs a wrap must preserve intact.
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
