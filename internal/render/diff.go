package render

import (
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

// A line is diff text only because a tool's style row or a fence said so.

var diffFences = map[string]bool{"diff": true, "figdiff": true, "patch": true}

// Indented fences count.
func fenceTag(line string) (string, bool) {
	t := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(strings.Trim(t[3:], "`~"))), true
}

func IsDiffFence(line string) bool {
	tag, ok := fenceTag(line)
	return ok && diffFences[tag]
}

// FencedLine is one source line; fence lines themselves are omitted.
type FencedLine struct {
	Text string
	Diff bool
}

// A diff region opens on a diff fence and closes on the next fence of any
// kind or the end of the text; other fences are left in place.
func FencedLines(text string) []FencedLine {
	raw := strings.Split(text, "\n")
	out := make([]FencedLine, 0, len(raw))
	inDiff := false
	for _, l := range raw {
		tag, isFence := fenceTag(l)
		switch {
		case isFence && inDiff:
			inDiff = false
			continue
		case isFence && diffFences[tag]:
			inDiff = true
			continue
		}
		out = append(out, FencedLine{Text: l, Diff: inDiff})
	}
	return out
}

func HasDiff(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if IsDiffFence(l) {
			return true
		}
	}
	return false
}

// DiffPaint returns a painter so a wrapped continuation keeps its source line's side.
func DiffPaint(src string) func(string) string {
	switch {
	case strings.HasPrefix(src, "+++"), strings.HasPrefix(src, "---"):
		return term.Label
	case strings.HasPrefix(src, "@@"):
		return term.Cyan
	case strings.HasPrefix(src, "+"):
		return term.DiffAdd
	case strings.HasPrefix(src, "-"):
		return term.DiffDel
	}
	return func(s string) string { return s }
}

// DiffRows paints a diff into rows no wider than width.
func DiffRows(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, src := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		paint := DiffPaint(src)
		for _, w := range wrapPlain(src, width) {
			out = append(out, paint(w))
		}
	}
	return out
}
