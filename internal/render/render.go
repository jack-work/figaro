// Package render turns a markdown string into ANSI terminal rows via
// glamour (tables, code blocks, syntax highlighting). It is a pure,
// deterministic function of (markdown, width): identical inputs yield
// identical rows, no retained state, no I/O. The CLI consumer holds the
// rows and line-diffs them; the web consumer ignores this package.
package render

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"github.com/jack-work/figaro/internal/livedoc"
)

// SpinnerFrames is the braille animation set, shared with the renderers.
var SpinnerFrames = livedoc.SpinnerFrames

// Prose renders a full markdown string through glamour — prose, lists,
// tables, and fenced code blocks all get glamour's styling (indent,
// surrounding blank lines, chroma syntax highlighting). A trailing
// unclosed fence (mid-stream) is synth-closed so a code block renders with
// a stable structure as it streams in.
//
// Every returned row is run through SanitizeForTerminal so embedded
// terminal-state escapes (alt-screen, cursor visibility, line wrap,
// mouse modes, OSC) from tool output or model-emitted text can never
// reach the host terminal.
func Prose(md string, width int) []string {
	if strings.Count(md, "```")%2 == 1 {
		md += "\n```"
	}
	return SanitizeRows(renderMarkdown(md, width))
}

// renderMarkdown renders markdown via glamour. Output rows are glamour's
// word-wrapped lines with surrounding blank padding trimmed; on a glamour
// failure it falls back to plain wrapped text so the live region never
// blanks.
func renderMarkdown(text string, width int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	r := rendererFor(width)
	out, err := r.Render(text)
	if err != nil || out == "" {
		var rows []string
		for _, l := range strings.Split(text, "\n") {
			rows = append(rows, wrapPlain(l, width)...)
		}
		return trimBlankEdges(rows)
	}
	return trimBlankEdges(strings.Split(strings.TrimRight(out, "\n"), "\n"))
}

// rendererCache memoizes one glamour renderer per width. Construction
// parses the style; output stays a pure function of (text, width).
var (
	rendererMu    sync.Mutex
	rendererCache = map[int]*glamour.TermRenderer{}
)

func rendererFor(width int) *glamour.TermRenderer {
	rendererMu.Lock()
	defer rendererMu.Unlock()
	if r, ok := rendererCache[width]; ok {
		return r
	}
	// The dark style adds a 2-column document margin on top of the wrap
	// width, so glamour emits rows up to width+2 wide. Wrap to width-2 so
	// rendered rows fit within width — a row that overflows the viewport
	// auto-wraps in the terminal and desyncs the live painter's
	// one-row-per-line cursor math.
	wrap := width - 2
	if wrap < 1 {
		wrap = 1
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithColorProfile(termenv.TrueColor), // pinned: determinism, not env-detected
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		// Width-only fallback; should not happen with a standard style.
		r, _ = glamour.NewTermRenderer(glamour.WithWordWrap(wrap))
	}
	rendererCache[width] = r
	return r
}

// wrapPlain hard-wraps a plain (no-ANSI) line to width display columns.
func wrapPlain(line string, width int) []string {
	if width <= 0 || runewidth.StringWidth(line) <= width {
		return []string{line}
	}
	var rows []string
	var b strings.Builder
	col := 0
	for _, r := range line {
		w := runewidth.RuneWidth(r)
		if col+w > width {
			rows = append(rows, b.String())
			b.Reset()
			col = 0
		}
		b.WriteRune(r)
		col += w
	}
	if b.Len() > 0 || len(rows) == 0 {
		rows = append(rows, b.String())
	}
	return rows
}

func trimBlankEdges(rows []string) []string {
	for len(rows) > 0 && strings.TrimSpace(rows[0]) == "" {
		rows = rows[1:]
	}
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	return rows
}
