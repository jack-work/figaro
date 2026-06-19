// Package render is the pure markdown→ANSI-rows renderer for the live
// document model. Render is a deterministic function of (markdown,
// Options): identical inputs yield identical Lines. No retained parser
// state, no I/O — the CLU consumer holds the rows and line-diffs them;
// the web consumer ignores this package entirely.
//
// Two rendering regimes, chosen per fenced block:
//   - bash-family fences (tool output) → plain monospace, clamped to the
//     last BashCap source lines with a "last N of M" header. No glamour,
//     no syntax highlighting.
//   - everything else (prose, tables, non-bash code) → glamour, which
//     gives tables and chroma syntax highlighting.
//
// A reserved spinner sentinel rune in the blob renders as the braille
// frame for Options.Tick, so animation is a local render concern and
// never touches the wire (the producer emits the sentinel once).
package render

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// SpinnerSentinel is the reserved rune (Unicode PUA) the producer writes
// into the blob to mark a running spinner. Render replaces it with the
// braille frame for Options.Tick. Shared with the producer/composer.
const SpinnerSentinel = ''

// SpinnerFrames is the braille animation set, shared with the web client.
var SpinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

const defaultBashCap = 10

var bashLangs = map[string]bool{
	"bash": true, "sh": true, "shell": true, "zsh": true, "console": true, "log": true,
}

// Options configures a render. Width is required on the live path.
type Options struct {
	Width   int    // viewport columns
	BashCap int    // max bash source lines shown; <=0 means default (10)
	Tick    uint64 // spinner animation frame
}

// Result is the rendered output: physical wrapped rows, each fully
// ANSI-styled, none with a trailing newline.
//
// StableRows is the number of leading rows guaranteed not to change
// again this turn — the commit watermark. A row is stable if its block
// is final: no running spinner sentinel, a closed bash fence, and not
// the last (still-active) block. The consumer flushes Lines[:StableRows]
// to scrollback once and only ever line-diffs Lines[StableRows:].
type Result struct {
	Lines      []string
	StableRows int
}

// Render maps a markdown blob to terminal rows at the given width.
func Render(md string, opts Options) Result {
	width := opts.Width
	if width <= 0 {
		width = 80
	}
	cap := opts.BashCap
	if cap <= 0 {
		cap = defaultBashCap
	}

	segs := splitSegments(md)
	firstLive := liveSegmentIndex(segs)

	var lines []string
	stableRows := 0
	for i, seg := range segs {
		var rows []string
		if seg.bash {
			rows = renderBash(seg.lang, seg.body, cap, width)
		} else {
			rows = renderMarkdown(seg.text, width, opts.Tick)
		}
		if i < firstLive {
			stableRows += len(rows)
		}
		lines = append(lines, rows...)
	}
	if lines == nil {
		lines = []string{}
	}
	if stableRows > len(lines) {
		stableRows = len(lines)
	}
	return Result{Lines: lines, StableRows: stableRows}
}

// liveSegmentIndex returns the index of the first segment that is still
// mutating — one with a running spinner sentinel or an unclosed bash
// fence, else the last segment (always treated as live; it's the active
// tail). Everything before it is stable. Returns len(segs) for none.
func liveSegmentIndex(segs []segment) int {
	for i, s := range segs {
		if !s.bash && strings.ContainsRune(s.text, SpinnerSentinel) {
			return i
		}
		if s.bash && !s.closed {
			return i
		}
	}
	if len(segs) == 0 {
		return 0
	}
	return len(segs) - 1
}

// segment is a maximal run of the blob: either a bash-family fence or a
// markdown chunk (which may itself contain non-bash fences).
type segment struct {
	bash   bool
	lang   string
	body   []string // bash: raw source lines
	text   string   // markdown: reconstructed source
	closed bool     // bash: closing fence seen (vs. still streaming)
}

// splitSegments walks the blob, peeling bash fences into their own
// segments and accumulating everything else into markdown segments.
// Unclosed trailing fences are kept (streaming); non-bash fences are
// synth-closed so glamour can highlight the partial.
func splitSegments(md string) []segment {
	lines := strings.Split(md, "\n")
	var segs []segment
	var mdbuf []string
	flush := func() {
		if len(mdbuf) > 0 {
			segs = append(segs, segment{text: strings.Join(mdbuf, "\n")})
			mdbuf = nil
		}
	}
	for i := 0; i < len(lines); {
		line := lines[i]
		if isFence(line) {
			lang := fenceLang(line)
			i++
			var body []string
			for i < len(lines) && !isFence(lines[i]) {
				body = append(body, lines[i])
				i++
			}
			closed := i < len(lines) // stopped on a closing fence, not EOF
			if closed {              // consume it
				i++
			}
			if bashLangs[lang] {
				flush()
				segs = append(segs, segment{bash: true, lang: lang, body: body, closed: closed})
			} else {
				mdbuf = append(mdbuf, line)
				mdbuf = append(mdbuf, body...)
				mdbuf = append(mdbuf, "```") // synth-close (closed or streaming)
			}
			continue
		}
		mdbuf = append(mdbuf, line)
		i++
	}
	flush()
	return segs
}

func isFence(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "```") }

func fenceLang(line string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "```")))
}

// renderBash renders a tool-output fence: clamp to the last cap source
// lines, hard-wrap each to width, and prepend a dim header.
func renderBash(lang string, body []string, cap, width int) []string {
	// Drop a single trailing empty line (composers tend to end with \n).
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	total := len(body)
	shown := body
	truncated := false
	if total > cap {
		shown = body[total-cap:]
		truncated = true
	}
	if lang == "" {
		lang = "bash"
	}
	var header string
	if truncated {
		header = lang + " · last " + itoa(cap) + " of " + itoa(total) + " lines"
	} else {
		header = lang + " · " + itoa(total) + " line" + plural(total)
	}
	out := []string{dim(header)}
	for _, l := range shown {
		out = append(out, wrapPlain(l, width)...)
	}
	return out
}

// renderMarkdown renders a non-bash chunk via glamour (tables, syntax
// highlighting), after substituting the spinner sentinel for the
// current braille frame. Output rows are glamour's word-wrapped lines
// with surrounding blank padding trimmed.
func renderMarkdown(text string, width int, tick uint64) []string {
	text = substituteSpinner(text, tick)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	r := rendererFor(width)
	out, err := r.Render(text)
	if err != nil || out == "" {
		// Fallback: plain wrapped text so a glamour failure never blanks
		// the live region.
		var rows []string
		for _, l := range strings.Split(text, "\n") {
			rows = append(rows, wrapPlain(l, width)...)
		}
		return trimBlankEdges(rows)
	}
	return trimBlankEdges(strings.Split(strings.TrimRight(out, "\n"), "\n"))
}

func substituteSpinner(text string, tick uint64) string {
	if !strings.ContainsRune(text, SpinnerSentinel) {
		return text
	}
	frame := string(SpinnerFrames[int(tick%uint64(len(SpinnerFrames)))])
	return strings.ReplaceAll(text, string(SpinnerSentinel), frame)
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
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithColorProfile(termenv.TrueColor), // pinned: determinism, not env-detected
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// Width-only fallback; should not happen with a standard style.
		r, _ = glamour.NewTermRenderer(glamour.WithWordWrap(width))
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

func dim(s string) string { return "\x1b[2m" + s + "\x1b[0m" }
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
