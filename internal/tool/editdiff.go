package tool

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// EditOp is a single find/replace pair.
type EditOp struct {
	OldText string
	NewText string
}

// appliedEdits holds the result of matching and applying EditOps.
type appliedEdits struct {
	baseContent string
	newContent  string
}

// applyEditsToNormalized matches and applies edits against LF-normalized
// content. Falls back to fuzzy-normalized matching if needed.
func applyEditsToNormalized(normalized string, edits []EditOp, displayPath string) (appliedEdits, error) {
	if len(edits) == 0 {
		return appliedEdits{}, fmt.Errorf("edits must contain at least one replacement")
	}

	// Normalize to LF.
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

	// Try exact match first, fall back to fuzzy.
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

// DiffResult holds a human-readable diff and the first changed line.
type DiffResult struct {
	Diff             string
	FirstChangedLine int
}

// splitForDiff splits by newline, dropping trailing empty element.
func splitForDiff(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// GenerateDiff produces a line-numbered diff. contextLines defaults
// to 4 if <= 0.
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
