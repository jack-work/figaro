package cli

import (
	"regexp"
	"strings"
	"testing"
)

// The byte-walking literal path must agree with a regex over the visible text.
func searchContainsReference(row, q string) bool {
	if q == "" {
		return false
	}
	expr := regexp.QuoteMeta(q)
	if !hasUpper(q) {
		expr = "(?i)" + expr
	}
	return regexp.MustCompile(expr).MatchString(visibleOf(row))
}

var searchRows = append([]string{
	"",
	"plain row",
	"\x1b[2mdim row\x1b[0m",
	"\x1b[2m  │ \x1b[0m   12  internal/cli/transcript.go:7: captured output",
	"tr\x1b[0manscript",
	"\x1b[7m",
	"\x1b[7",
	"\x1b",
	"a\x1bZb",
	"7m and \x1b[7m",
	"\x1b[38;5;238m日本語\x1b[0mtail",
	"aaa\x1b[0maaab",
}, clipCorpus...)

var searchQueries = []string{
	"", "a", "b", "aaab", "transcript", "tr", "7m", "[7m", "\x1b", "0m",
	"日本語", "captured output", "internal/cli", "xyz", " ", "  │ ",
}

func TestSearchContainsMatchesReference(t *testing.T) {
	for _, row := range searchRows {
		for _, q := range searchQueries {
			if regexp.QuoteMeta(q) != q {
				continue
			}
			if got, want := searchContains(row, q), searchContainsReference(row, q); got != want {
				t.Errorf("searchContains(%q, %q) = %v, want %v", row, q, got, want)
			}
		}
	}
}

func TestHighlightKeepsTheTextAndMarksEveryMatch(t *testing.T) {
	for _, row := range searchRows {
		for _, q := range searchQueries {
			got := highlightMatches(row, q)
			if visibleOf(got) != visibleOf(row) {
				t.Errorf("highlightMatches(%q, %q) changed the text: %q", row, q, visibleOf(got))
			}
			if q == "" {
				continue
			}
			want := len(patternOf(q).spans(visibleOf(row)))
			if n := strings.Count(got, matchOn) - strings.Count(row, matchOn); n != want {
				t.Errorf("highlightMatches(%q, %q) opened %d bands for %d matches", row, q, n, want)
			}
			if changed := got != row; changed != searchContains(row, q) {
				t.Errorf("highlightMatches(%q, %q) changed=%v, contains=%v", row, q, changed, searchContains(row, q))
			}
		}
	}
}
