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

	"github.com/charmbracelet/glamour"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// Prose renders a full markdown string through glamour — prose, lists,
// tables, and fenced code blocks all get glamour's styling (indent,
// surrounding blank lines, chroma syntax highlighting). A trailing
// unclosed fence (mid-stream) is synth-closed so a code block renders with
// a stable structure as it streams in.
//
// Results are memoized (see prose_cache.go): the output is a pure function of
// (markdown, width), and the callers ask for the same block over and over.
//
// Every returned row is run through SanitizeForTerminal so embedded
// terminal-state escapes (alt-screen, cursor visibility, line wrap,
// mouse modes, OSC) from tool output or model-emitted text can never
// reach the host terminal.
func Prose(md string, width int) []string {
	if strings.Count(md, "```")%2 == 1 {
		md += "\n```"
	}
	if rows, ok := lookupProse(md, width); ok {
		return rows
	}
	rows := SanitizeRows(renderMarkdown(md, width))
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
	//
	// Observed: a table row carrying more cells than the table's Alignments
	// ("index out of range [3] with length 3" in TableElement.setStyles). That
	// particular one is fixed by the lock below, but glamour parses untrusted
	// model output and this is the boundary where that stops being fatal.
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
//
// A glamour TermRenderer IS NOT SAFE FOR CONCURRENT USE: it accumulates state
// on itself while walking the document — the block stack, and a table's rows
// and headers, reset only in the table's Finish. Two goroutines rendering at
// the same width shared one cached renderer and interleaved cells into a single
// row; the row then had more cells than the table's Alignments, which glamour
// indexes by column, and it panicked.
//
// The mutex used to guard only the cache lookup and was released before the
// caller rendered, which protected the map and nothing else. Holding it across
// Render serializes rendering per process. That is acceptable because Prose is
// memoized (lookupProse) so repeat frames never reach here, and because the
// alternative — a renderer per call — re-parses the style sheet every time.
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
// rendererMu, and must keep holding it across Render — see renderLocked.
func rendererForLocked(width int) *glamour.TermRenderer {
	if r, ok := rendererCache[width]; ok {
		return r
	}
	// glamour counts the dark style's 2-column document margin INSIDE the
	// word-wrap budget: WithWordWrap(n) yields rows n-2 columns wide.
	// (Through glamour v0.8.0 the margin was added ON TOP, so the same call
	// yielded rows n+2 wide and this compensation was width-2.) Asking for
	// width+2 therefore lands rows at exactly width, which is the ceiling —
	// a row that overflows the viewport auto-wraps in the terminal and
	// desyncs the live painter's one-row-per-line cursor math.
	//
	// Measured across widths 8..140 on tables with CJK, code spans and
	// three columns: no table row exceeds width at this bias (the probe is
	// TestProse_TableRowsHoldPainterInvariant). Prose with an UNBREAKABLE
	// token still overruns at any bias — glamour will not hyphenate — which
	// is why every caller clips (clipToWidth) and why that is not this
	// function's job.
	wrap := width + 2
	if wrap < 1 {
		wrap = 1
	}
	opts := []glamour.TermRendererOption{
		glamour.WithStandardStyle("dark"),
		glamour.WithColorProfile(termenv.TrueColor), // pinned: determinism, not env-detected
		glamour.WithWordWrap(wrap),
		// THE TABLE FIX, stated explicitly rather than left to the default.
		// glamour's table renderer used to give every cell a lipgloss style
		// with Inline(true), which disables word wrap in the cell render,
		// while lipgloss/table sized each row to the WRAPPED height of its
		// content: a cell needing two lines got two lines of space, its
		// first line of text, and a blank — the remainder discarded by the
		// cell's MaxWidth. Table text was destroyed here, upstream of
		// anything a view could do about it.
		//
		// This is also the one lever for the wrap-vs-truncate taste call:
		// WithTableWrap(false) restores single-line cells, but truncated
		// with an explicit "…" rather than silently blanked.
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

// visiblyBlank reports whether a row paints nothing — whitespace once ANSI
// escapes are discounted. A TrimSpace test is not enough: glamour pads a
// block_quote with a leading row of STYLED spaces, and that row survived the
// edge trim, so a thinking block (a blockquote) carried its own blank on top
// of the separator the node renderers already put between blocks — two blank
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
