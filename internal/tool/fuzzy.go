package tool

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizeForFuzzyMatch applies the progressive transformations that
// let an edit match even when the LLM mangled quote characters, dashes,
// or whitespace:
//   - NFKC normalization (collapses full-width forms, compatibility chars, ...)
//   - Trailing whitespace stripped per line
//   - Smart single/double quotes -> ASCII
//   - Unicode dashes/hyphens -> ASCII '-'
//   - NBSP / various Unicode spaces -> regular space
//
// The output is deterministic and is used both for searching and for
// the replacement base when fuzzy matching kicks in.
func NormalizeForFuzzyMatch(text string) string {
	text = norm.NFKC.String(text)

	// Strip trailing whitespace per line without allocating when nothing
	// changes. strings.Builder + Split is fine for our file sizes.
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	text = strings.Join(lines, "\n")

	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		// Smart single quotes, low-9, high-reversed-9.
		case '\u2018', '\u2019', '\u201A', '\u201B':
			b.WriteByte('\'')
		// Smart double quotes, low-9, high-reversed-9.
		case '\u201C', '\u201D', '\u201E', '\u201F':
			b.WriteByte('"')
		// Hyphen, non-breaking hyphen, figure dash, en dash, em dash,
		// horizontal bar, minus sign.
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			b.WriteByte('-')
		// NBSP, en/em/figure/punctuation/thin/hair spaces, narrow NBSP,
		// medium math space, ideographic space.
		case '\u00A0',
			'\u2002', '\u2003', '\u2004', '\u2005', '\u2006',
			'\u2007', '\u2008', '\u2009', '\u200A',
			'\u202F', '\u205F', '\u3000':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FuzzyMatchResult describes a FuzzyFind result. When UsedFuzzyMatch is
// true, Index/MatchLength refer to positions in ContentForReplacement,
// not in the original content. Callers that want to edit should use
// ContentForReplacement as the base for their replacement.
type FuzzyMatchResult struct {
	Found                 bool
	Index                 int
	MatchLength           int
	UsedFuzzyMatch        bool
	ContentForReplacement string
}

// FuzzyFind tries to locate needle in haystack. An exact substring match
// wins. If that fails, both sides are normalized via NormalizeForFuzzyMatch
// and the search is retried in the normalized space.
func FuzzyFind(haystack, needle string) FuzzyMatchResult {
	if i := strings.Index(haystack, needle); i != -1 {
		return FuzzyMatchResult{
			Found:                 true,
			Index:                 i,
			MatchLength:           len(needle),
			UsedFuzzyMatch:        false,
			ContentForReplacement: haystack,
		}
	}

	normHay := NormalizeForFuzzyMatch(haystack)
	normNeedle := NormalizeForFuzzyMatch(needle)
	if i := strings.Index(normHay, normNeedle); i != -1 {
		return FuzzyMatchResult{
			Found:                 true,
			Index:                 i,
			MatchLength:           len(normNeedle),
			UsedFuzzyMatch:        true,
			ContentForReplacement: normHay,
		}
	}

	return FuzzyMatchResult{Found: false, ContentForReplacement: haystack}
}

// CountFuzzyOccurrences returns how many times needle appears in
// haystack after both are fuzzy-normalized. Used for uniqueness checks.
func CountFuzzyOccurrences(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	return strings.Count(NormalizeForFuzzyMatch(haystack), NormalizeForFuzzyMatch(needle))
}
