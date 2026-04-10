package tool

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// EditOp is a single find/replace pair within an edit request. All ops
// in a multi-edit call are matched against the same original content.
type EditOp struct {
	OldText string
	NewText string
}

// appliedEdits is the intermediate result of matching, sorting, and
// applying a batch of EditOps. baseContent is what we diffed against
// (either the LF-normalized content, or the fuzzy-normalized version
// if any edit needed fuzzy matching). newContent is post-application.
type appliedEdits struct {
	baseContent string
	newContent  string
}

// applyEditsToNormalized runs a batch of edits against already-LF-normalized
// content. It enforces uniqueness, non-overlap, and at-least-one change.
//
// Match semantics:
//   - Each edit is matched against the SAME baseContent, not incrementally.
//     This means the model provides oldText slices as they appear in the
//     original file, without having to reason about earlier edits.
//   - Replacements are applied in reverse offset order so earlier edit
//     offsets stay valid.
//   - If any edit needs fuzzy matching, the baseContent switches to the
//     fuzzy-normalized form for ALL edits. The resulting file will have
//     normalized quotes/dashes/whitespace — an acceptable side effect of
//     fixing formatting drift.
func applyEditsToNormalized(normalized string, edits []EditOp, displayPath string) (appliedEdits, error) {
	if len(edits) == 0 {
		return appliedEdits{}, fmt.Errorf("edits must contain at least one replacement")
	}

	// Normalize every oldText/newText to LF so the model doesn't have to
	// care about line endings.
	normEdits := make([]EditOp, len(edits))
	for i, e := range edits {
		if e.OldText == "" {
			return appliedEdits{}, emptyOldTextError(displayPath, i, len(edits))
		}
		normEdits[i] = EditOp{
			OldText: NormalizeToLF(e.OldText),
			NewText: NormalizeToLF(e.NewText),
		}
	}

	// Probe each edit against the raw normalized content first. If any
	// match falls through to fuzzy, rebase everything onto the
	// fuzzy-normalized content.
	initialNeedsFuzzy := false
	for _, e := range normEdits {
		m := FuzzyFind(normalized, e.OldText)
		if m.Found && m.UsedFuzzyMatch {
			initialNeedsFuzzy = true
			break
		}
	}
	base := normalized
	if initialNeedsFuzzy {
		base = NormalizeForFuzzyMatch(normalized)
	}

	type matched struct {
		editIndex int
		start     int
		length    int
		newText   string
	}
	resolved := make([]matched, 0, len(normEdits))
	for i, e := range normEdits {
		m := FuzzyFind(base, e.OldText)
		if !m.Found {
			return appliedEdits{}, notFoundError(displayPath, i, len(normEdits))
		}
		// Uniqueness check. Count in the (possibly fuzzy) base space.
		occ := CountFuzzyOccurrences(base, e.OldText)
		if occ > 1 {
			return appliedEdits{}, duplicateMatchError(displayPath, i, len(normEdits), occ)
		}
		resolved = append(resolved, matched{
			editIndex: i,
			start:     m.Index,
			length:    m.MatchLength,
			newText:   e.NewText,
		})
	}

	sort.Slice(resolved, func(i, j int) bool { return resolved[i].start < resolved[j].start })
	for i := 1; i < len(resolved); i++ {
		prev := resolved[i-1]
		cur := resolved[i]
		if prev.start+prev.length > cur.start {
			return appliedEdits{}, fmt.Errorf(
				"edits[%d] and edits[%d] overlap in %s. merge them into one edit or target disjoint regions",
				prev.editIndex, cur.editIndex, displayPath,
			)
		}
	}

	// Apply in reverse order so earlier offsets remain valid.
	out := base
	for i := len(resolved) - 1; i >= 0; i-- {
		r := resolved[i]
		out = out[:r.start] + r.newText + out[r.start+r.length:]
	}

	if out == base {
		return appliedEdits{}, noChangeError(displayPath, len(normEdits))
	}
	return appliedEdits{baseContent: base, newContent: out}, nil
}

// --- error helpers (tuned to match pi-mono's messages loosely so the
// model gets the same actionable hints) ---

func notFoundError(path string, idx, total int) error {
	if total == 1 {
		return fmt.Errorf("could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
	}
	return fmt.Errorf("could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines.", idx, path)
}

func duplicateMatchError(path string, idx, total, occurrences int) error {
	if total == 1 {
		return fmt.Errorf("found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.", occurrences, path)
	}
	return fmt.Errorf("found %d occurrences of edits[%d] in %s. Each oldText must be unique. Please provide more context to make it unique.", occurrences, idx, path)
}

func emptyOldTextError(path string, idx, total int) error {
	if total == 1 {
		return fmt.Errorf("oldText must not be empty in %s", path)
	}
	return fmt.Errorf("edits[%d].oldText must not be empty in %s", idx, path)
}

func noChangeError(path string, total int) error {
	if total == 1 {
		return fmt.Errorf("no changes made to %s. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected.", path)
	}
	return fmt.Errorf("no changes made to %s. The replacements produced identical content.", path)
}

// --- diff rendering ---

// DiffResult is returned by GenerateDiff. Diff is a human-readable,
// line-numbered unified-ish diff; FirstChangedLine is the 1-indexed
// line in the new file where the first change appears (useful for
// editors that want to jump there). FirstChangedLine is 0 if the
// two sides are identical.
type DiffResult struct {
	Diff             string
	FirstChangedLine int
}

// splitForDiff splits by '\n' and drops a trailing empty element so
// "a\nb\n" becomes ["a","b"] instead of ["a","b",""]. Empty input
// becomes a single empty line for stable comparison.
func splitForDiff(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// GenerateDiff produces a compact line-numbered unified-ish diff
// between oldContent and newContent. contextLines controls how many
// surrounding unchanged lines are shown around each change; passing
// <= 0 uses a sensible default of 4.
//
// NOTE (LCS learning TODO): this currently delegates the actual
// sequence matching to go-difflib's SequenceMatcher, which uses the
// Ratcliff/Obershelp "gestalt pattern matching" algorithm rather than
// classic longest-common-subsequence. The rendering layer on top is
// all ours. Revisit and hand-roll an LCS-based matcher here once you
// want to learn the algorithm — the public API (GenerateDiff in/out)
// can stay unchanged.
func GenerateDiff(oldContent, newContent string, contextLines int) DiffResult {
	if contextLines <= 0 {
		contextLines = 4
	}

	oldLines := splitForDiff(oldContent)
	newLines := splitForDiff(newContent)

	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	width := len(strconv.Itoa(maxLineNum))
	if width == 0 {
		width = 1
	}

	matcher := difflib.NewMatcher(oldLines, newLines)
	ops := matcher.GetOpCodes()

	var out []string
	firstChanged := 0

	fmtLine := func(prefix string, num int, text string) string {
		return fmt.Sprintf("%s%*d %s", prefix, width, num, text)
	}
	ellipsis := fmt.Sprintf(" %s ...", strings.Repeat(" ", width))

	for i, op := range ops {
		if op.Tag == 'e' {
			hasLeading := i > 0
			hasTrailing := i < len(ops)-1
			segment := newLines[op.J1:op.J2]
			oldNum := op.I1 + 1

			switch {
			case hasLeading && hasTrailing:
				if len(segment) <= contextLines*2 {
					for _, line := range segment {
						out = append(out, fmtLine(" ", oldNum, line))
						oldNum++
					}
				} else {
					for _, line := range segment[:contextLines] {
						out = append(out, fmtLine(" ", oldNum, line))
						oldNum++
					}
					out = append(out, ellipsis)
					tailStart := len(segment) - contextLines
					oldNum = op.I1 + 1 + tailStart
					for _, line := range segment[tailStart:] {
						out = append(out, fmtLine(" ", oldNum, line))
						oldNum++
					}
				}
			case hasLeading:
				show := segment
				truncated := false
				if len(show) > contextLines {
					show = show[:contextLines]
					truncated = true
				}
				for _, line := range show {
					out = append(out, fmtLine(" ", oldNum, line))
					oldNum++
				}
				if truncated {
					out = append(out, ellipsis)
				}
			case hasTrailing:
				tail := segment
				if len(tail) > contextLines {
					tail = tail[len(tail)-contextLines:]
					out = append(out, ellipsis)
					oldNum = op.I2 - len(tail) + 1
				}
				for _, line := range tail {
					out = append(out, fmtLine(" ", oldNum, line))
					oldNum++
				}
			}
			continue
		}

		// Non-equal op — delete, insert, or replace.
		if firstChanged == 0 {
			firstChanged = op.J1 + 1
		}
		if op.Tag == 'd' || op.Tag == 'r' {
			for k := op.I1; k < op.I2; k++ {
				out = append(out, fmtLine("-", k+1, oldLines[k]))
			}
		}
		if op.Tag == 'i' || op.Tag == 'r' {
			for k := op.J1; k < op.J2; k++ {
				out = append(out, fmtLine("+", k+1, newLines[k]))
			}
		}
	}

	return DiffResult{
		Diff:             strings.Join(out, "\n"),
		FirstChangedLine: firstChanged,
	}
}
