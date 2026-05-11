package tool

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizeForFuzzyMatch normalizes quotes, dashes, whitespace, and
// Unicode forms so edits match despite character mangling.
func NormalizeForFuzzyMatch(text string) string {
	text = norm.NFKC.String(text)

	// Strip trailing whitespace per line.
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	text = strings.Join(lines, "\n")

	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {

		case '\u2018', '\u2019', '\u201A', '\u201B':
			b.WriteByte('\'')

		case '\u201C', '\u201D', '\u201E', '\u201F':
			b.WriteByte('"')

		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			b.WriteByte('-')

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

// FuzzyMatchResult describes a FuzzyFind result.
type FuzzyMatchResult struct {
	Found                 bool
	Index                 int
	MatchLength           int
	UsedFuzzyMatch        bool
	ContentForReplacement string
}

// FuzzyFind locates needle in haystack, falling back to normalized match.
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

// CountFuzzyOccurrences counts matches after normalization.
func CountFuzzyOccurrences(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	return strings.Count(NormalizeForFuzzyMatch(haystack), NormalizeForFuzzyMatch(needle))
}
