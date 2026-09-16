package render

import (
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

// A DIFF IS DRAWN ONE WAY, EVERYWHERE FIGARO DRAWS ONE: the `edit` tool's
// result, a fenced region of any other tool's output, a fenced region of the
// agent's prose. All three paint through DiffPaint, so no caller decides for
// itself what a `+` looks like.
//
// Nothing here GUESSES. A line is diff text only because something upstream
// said so: the tool's style row, or a fence.

// diffFences are the info strings that open a diff region. `diff` and `patch`
// are what the rest of the world writes; `figdiff` asks for figaro's picture
// by name, whatever a future markdown pipeline decides `diff` should mean.
var diffFences = map[string]bool{"diff": true, "figdiff": true, "patch": true}

// fenceTag reports a fence line's info string, and whether the line is a fence
// at all. Indented fences count: output is often indented by whatever printed
// it.
func fenceTag(line string) (string, bool) {
	t := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(strings.Trim(t[3:], "`~"))), true
}

// IsDiffFence reports that this line opens (or closes) a diff region.
func IsDiffFence(line string) bool {
	tag, ok := fenceTag(line)
	return ok && diffFences[tag]
}

// FencedLine is one source line of a body and what it is: prose to be shown as
// it arrived, or a line of a diff. Fence lines are not among them: they are
// punctuation, and drawing them would put the markup on the screen.
type FencedLine struct {
	Text string
	Diff bool
}

// FencedLines splits a body into its diff regions and everything else. A region
// opens on a diff fence and closes on the next fence of any kind (or at the
// end of the text: a stream cut mid-block still draws as a diff).
//
// A fence that is not a diff fence is left alone, text and all: this scanner
// is not a markdown parser and has no opinion about ```go.
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

// HasDiff reports whether a body carries a diff region, so a caller that pays
// for the split only when there is something to split can ask first.
func HasDiff(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if IsDiffFence(l) {
			return true
		}
	}
	return false
}

// DiffPaint answers how one SOURCE line of a diff is coloured, as a painter
// rather than a painted string: a wrapped continuation keeps the side its
// source line was on. File headers are read before the +/- lines they begin
// with, or `---` comes out as a deletion of `--`.
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

// DiffRows paints a whole diff into rows no wider than width, wrapping long
// lines and keeping each row on its source line's side. It is the block form
// of DiffPaint, for callers that hold the text and want rows.
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
