// Package render turns a markdown string into ANSI terminal rows via
// glamour (tables, code blocks, syntax highlighting). It is a pure,
// deterministic function of (markdown, width): identical inputs yield
// identical rows, no retained state, no I/O. The CLI consumer holds the
// rows and line-diffs them; the web consumer ignores this package.
package render

import (
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/glamour/v2"
	"github.com/mattn/go-runewidth"
)

// Prose renders a full markdown string through glamour: prose, lists,
// tables, and fenced code blocks all get glamour's styling (indent,
// surrounding blank lines, chroma syntax highlighting). A trailing
// unclosed fence (mid-stream) is synth-closed so a code block renders with
// a stable structure as it streams in.
func Prose(md string, width int) []string {
	// Sanitize on the way IN. glamour drops the ESC byte of an escape it does
	// not understand and keeps the parameter bytes as visible text: while
	// having wrapped as though the whole sequence were zero-width: so
	// `\x1b[31mred` came back as a row four cells wider than asked for, at every
	// width, with "[31m" printed. Models paste ANSI out of tool output
	// constantly, so this is a live path, and a bare ESC could also shift a
	// gutter's column by riding along in the row prefix.
	md = StripEscapes(md)
	if strings.Count(md, "```")%2 == 1 {
		md += "\n```"
	}
	if rows, ok := lookupProse(md, width); ok {
		return rows
	}
	rows := hardWrapOverlong(SanitizeRows(renderMarkdown(md, width)), width)
	storeProse(md, width, rows)
	return append([]string(nil), rows...)
}

// renderMarkdown renders markdown via glamour. Output rows are glamour's
// word-wrapped lines with surrounding blank padding trimmed; on a glamour
// failure it falls back to plain wrapped text so the live region never
// blanks.
func renderMarkdown(text string, width int) (rows []string) {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	plain := func() []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			out = append(out, wrapPlain(l, width)...)
		}
		return trimBlankEdges(out)
	}
	// A glamour PANIC must not reach the caller. renderMarkdown already falls
	// back to plain text when Render returns an error, but a panic blew straight
	// through that, and figaro renders from detached goroutines (refreshQueued),
	// so it took the whole CLI down instead of spoiling one frame.
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("markdown render panicked; falling back to plain text",
				"panic", r, "width", width, "bytes", len(text))
			rows = plain()
		}
	}()
	out, err := renderLocked(text, width)
	if err != nil || out == "" {
		return plain()
	}
	return trimBlankEdges(strings.Split(strings.TrimRight(out, "\n"), "\n"))
}

// renderLocked renders under the renderer lock.
func renderLocked(text string, width int) (string, error) {
	rendererMu.Lock()
	defer rendererMu.Unlock()
	return rendererForLocked(width).Render(text)
}

// rendererCache memoizes one glamour renderer per width. Construction
// parses the style; output stays a pure function of (text, width).
var (
	rendererMu    sync.Mutex
	rendererCache = map[int]*glamour.TermRenderer{}
)

// rendererForLocked returns the memoized renderer for width. CALLER HOLDS
// rendererMu, and must keep holding it across Render: see renderLocked.
func rendererForLocked(width int) *glamour.TermRenderer {
	if r, ok := rendererCache[width]; ok {
		return r
	}
	// glamour counts the dark style's 2-column document margin INSIDE the
	// word-wrap budget: WithWordWrap(n) yields rows n-2 columns wide.
	// (Through glamour v0.8.0 the margin was added ON TOP, so the same call
	// yielded rows n+2 wide and this compensation was width-2.) Asking for
	// width+2 therefore lands rows at exactly width, which is the ceiling -
	// a row that overflows the viewport auto-wraps in the terminal and
	// desyncs the live painter's one-row-per-line cursor math.
	wrap := width + 2
	if wrap < 1 {
		wrap = 1
	}
	opts := []glamour.TermRendererOption{
		glamour.WithStandardStyle("dark"),
		// NO WithColorProfile. v1 had one and it was pinned to TrueColor for
		// determinism; v2 has no such option, because lipgloss emits the style's
		// own colour verbatim ("252" still comes out as ESC[38;5;252m) and any
		// downgrade happens at the writer, not here. So the output is as
		// deterministic as it was, by construction rather than by a pin.
		glamour.WithWordWrap(wrap),
		// THE TABLE FIX, stated explicitly rather than left to the default.
		// glamour's table renderer used to give every cell a lipgloss style
		// with Inline(true), which disables word wrap in the cell render,
		// while lipgloss/table sized each row to the WRAPPED height of its
		// content: a cell needing two lines got two lines of space, its
		// first line of text, and a blank: the remainder discarded by the
		// cell's MaxWidth. Table text was destroyed here, upstream of
		// anything a view could do about it.
		glamour.WithTableWrap(true),
	}
	r, err := glamour.NewTermRenderer(opts...)
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
	for len(rows) > 0 && visiblyBlank(rows[0]) {
		rows = rows[1:]
	}
	for len(rows) > 0 && visiblyBlank(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// visiblyBlank reports whether a row paints nothing: whitespace once ANSI
// escapes are discounted. A TrimSpace test is not enough: glamour pads a
// block_quote with a leading row of STYLED spaces, and that row survived the
// edge trim, so a thinking block (a blockquote) carried its own blank on top
// of the separator the node renderers already put between blocks: two blank
// rows where prose gets one, in every surface at once.
func visiblyBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == 0x1b { // escape sequence: skip through its final letter
			for i++; i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')); i++ {
			}
		} else if c > ' ' {
			return false
		}
	}
	return true
}

// cells is a row's width on screen: escapes cost nothing, wide runes cost two.
func cells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		n += runewidth.RuneWidth(r)
		i += sz
	}
	return n
}

// hardWrapOverlong is the fallback for content glamour will not break.
func hardWrapOverlong(rows []string, width int) []string {
	if width <= 0 {
		return rows
	}
	over := false
	for _, r := range rows {
		if cells(r) > width {
			over = true
			break
		}
	}
	if !over {
		return rows // same backing array, no allocation
	}
	out := make([]string, 0, len(rows)+2)
	for _, r := range rows {
		if cells(r) <= width {
			out = append(out, r)
			continue
		}
		out = append(out, splitToWidth(r, width)...)
	}
	return out
}

// splitToWidth cuts one row into chunks of at most width cells, carrying the
// active SGR run onto each continuation so a colour does not bleed or vanish.
func splitToWidth(row string, width int) []string {
	var out []string
	var chunk strings.Builder
	var style strings.Builder // SGR seen since the last reset
	col := 0
	flush := func() {
		if chunk.Len() > 0 {
			out = append(out, chunk.String())
			chunk.Reset()
		}
		col = 0
		if style.Len() > 0 {
			chunk.WriteString(style.String())
		}
	}
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			j := skipEscape(row, i)
			seq := row[i:j]
			chunk.WriteString(seq)
			if strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					style.Reset()
				} else {
					style.WriteString(seq)
				}
			}
			i = j
			continue
		}
		r, sz := utf8.DecodeRuneInString(row[i:])
		w := runewidth.RuneWidth(r)
		if col+w > width {
			flush()
		}
		chunk.WriteRune(r)
		col += w
		i += sz
	}
	if chunk.Len() > 0 {
		out = append(out, chunk.String())
	}
	return out
}
