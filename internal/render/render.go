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
	// Sanitize on the way IN. glamour drops the ESC byte of an escape it does
	// not understand and keeps the parameter bytes as visible text — while
	// having wrapped as though the whole sequence were zero-width — so
	// `\x1b[31mred` came back as a row four cells wider than asked for, at every
	// width, with "[31m" printed. Models paste ANSI out of tool output
	// constantly, so this is a live path, and a bare ESC could also shift a
	// gutter's column by riding along in the row prefix.
	//
	// StripEscapes, NOT SanitizeForTerminal: that one is an OUTPUT sanitizer and
	// deliberately keeps SGR verbatim, so it left this bug exactly as it was —
	// and I claimed otherwise for a whole commit because no test contradicted me.
	//
	// Fixing it here rather than clipping the result keeps the defect out of
	// the pipeline entirely: nothing downstream has to know that a row's byte
	// count and its cell count disagree.
	md = StripEscapes(md)
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
