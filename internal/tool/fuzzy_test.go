package tool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jack-work/figaro/internal/tool"
)

func TestNormalizeForFuzzyMatch_SmartQuotes(t *testing.T) {
	in := "He said \u201chello\u201d and \u2018hi\u2019"
	want := `He said "hello" and 'hi'`
	assert.Equal(t, want, tool.NormalizeForFuzzyMatch(in))
}

func TestNormalizeForFuzzyMatch_Dashes(t *testing.T) {
	in := "a\u2013b\u2014c\u2212d"
	assert.Equal(t, "a-b-c-d", tool.NormalizeForFuzzyMatch(in))
}

func TestNormalizeForFuzzyMatch_Nbsp(t *testing.T) {
	in := "foo\u00a0bar\u3000baz"
	assert.Equal(t, "foo bar baz", tool.NormalizeForFuzzyMatch(in))
}

func TestNormalizeForFuzzyMatch_TrailingWhitespace(t *testing.T) {
	in := "line1   \nline2\t\nline3"
	assert.Equal(t, "line1\nline2\nline3", tool.NormalizeForFuzzyMatch(in))
}

func TestNormalizeForFuzzyMatch_NFKCFullwidth(t *testing.T) {
	// NFKC collapses full-width ASCII to plain ASCII.
	in := "\uff21\uff22\uff23" // "ＡＢＣ"
	assert.Equal(t, "ABC", tool.NormalizeForFuzzyMatch(in))
}

func TestFuzzyFind_ExactMatchShortCircuit(t *testing.T) {
	r := tool.FuzzyFind("hello world", "world")
	assert.True(t, r.Found)
	assert.False(t, r.UsedFuzzyMatch)
	assert.Equal(t, 6, r.Index)
	assert.Equal(t, 5, r.MatchLength)
}

func TestFuzzyFind_FuzzyFallback(t *testing.T) {
	// File has a smart quote; needle uses ASCII.
	haystack := "title = \u201cFoo\u201d;"
	needle := `title = "Foo";`
	r := tool.FuzzyFind(haystack, needle)
	assert.True(t, r.Found)
	assert.True(t, r.UsedFuzzyMatch)
}

func TestFuzzyFind_NotFound(t *testing.T) {
	r := tool.FuzzyFind("hello", "zzz")
	assert.False(t, r.Found)
}

func TestCountFuzzyOccurrences(t *testing.T) {
	assert.Equal(t, 2, tool.CountFuzzyOccurrences("foo bar foo baz", "foo"))
	// Smart quotes in haystack collapse to ASCII before counting.
	assert.Equal(t, 2, tool.CountFuzzyOccurrences("\u201cx\u201d and \u201cx\u201d", `"x"`))
}
