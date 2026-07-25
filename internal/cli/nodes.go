package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

const (
	nodeBashCapDefault  = 10
	nodeOutputUnlimited = -1
	toolSummaryCap      = 80 // default truncation for a tool's summary line
)

// renderSettings is the consumer-side verbosity toggle. The wire/IR always
// carries the full data; this only affects display, so it can be flipped live
// (Ctrl-O) and the unit re-rendered. Thinking blocks are always shown (muted);
// verbose additionally expands tool inputs to the full wrapped command.
type renderSettings struct {
	verbose  bool
	jsonMode bool // -j / --json: emit a single {aria_id, ...} JSON line on stdout instead of a live render
	listen   bool // -l / --listen: auto-enter the transcript at startup
}

// renderNode draws ONE node. This is the single dispatch on node kind, shared
// by every view: `show` (via renderNodeList), the inline incipit and the
// transcript pager (via ariaView). It used to exist twice — ariaView switched
// on n.Type alone and so drew a turn's prompt as agent prose, putting the
// user's own question under the "‹ figaro" header while `show` correctly
// marked it "↳ you". Two renderers for one representation is the exact defect
// class turn addressing exists to remove; there is now one.
func renderNode(n livedoc.Node, width, bashCap int, tick uint64, verbose bool) []string {
	switch {
	case n.Type == livedoc.NodeTool:
		return renderToolNode(n, width, bashCap, tick, verbose)
	case n.Type == livedoc.NodeThinking:
		return renderThinkingNode(n, width)
	// The prompt and a steering interjection are the same kind of thing in
	// different positions, so they draw the same way (docs/turn-addressing.md).
	case n.Type == livedoc.NodeSteering, n.Role == "user":
		return renderSteeringNode(n, width)
	default:
		return renderProseNode(n, width)
	}
}

// renderNodeList renders a unit's whole node list to terminal rows. The list
// is walked uniformly — every tool renders through renderToolNode with no
// per-tool branching. One blank row separates adjacent blocks; a final
// clipToWidth pass keeps every row on a single physical line.
func renderNodeList(nodes []livedoc.Node, width, bashCap int, tick uint64, set renderSettings) []string {
	if width <= 0 {
		width = 80
	}
	if bashCap <= 0 {
		bashCap = nodeBashCapDefault
	}
	var rows []string
	for i, n := range nodes {
		// Empty prose/thinking nodes are minted by the projection so ids cannot
		// shift when a block fills (docs/turn-addressing.md, invariant 6).
		// Hiding them is the renderer's job, not the projection's.
		if n.Type != livedoc.NodeTool && strings.TrimSpace(n.Markdown) == "" {
			continue
		}
		var nr []string
		nr = renderNode(n, width, bashCap, tick, set.verbose)
		if i > 0 {
			nr = append([]string{""}, nr...)
		}
		rows = append(rows, nr...)
	}
	for i := range rows {
		rows[i] = clipToWidth(rows[i], width)
	}
	return rows
}

// clipToWidth truncates a styled row to at most width display columns,
// passing ANSI escape sequences through uncounted and appending a reset so
// a cut mid-color doesn't bleed. Embedded control characters (newlines,
// tabs, CR) are flattened to spaces: a row must be exactly one physical
// line or it desyncs the painter's one-row-per-line cursor math (a
// multi-line bash command in a tool's arg summary is the common culprit).
//
// The overwhelmingly common case is a row that already fits and carries
// nothing to rewrite — every row of every frame is clipped, twice (once by
// the node renderer, once by the transcript's selection gutter), and almost
// none of them actually need clipping. clipFits proves the rewrite is a
// no-op with a single allocation-free byte scan; only rows that genuinely
// change go through clipToWidthRewrite.
func clipToWidth(s string, width int) string {
	if clipFits(s, width) {
		return s
	}
	return clipToWidthRewrite(s, width)
}

// clipFits reports whether clipToWidthRewrite(s, width) == s, i.e. whether
// the row is already within width, free of control characters, and valid
// UTF-8 (the rewrite decodes to runes, so an invalid byte would come back
// as U+FFFD and change the row). Pure scan: no allocation.
func clipFits(s string, width int) bool {
	col := 0
	for i := 0; i < len(s); {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			// Run of printable ASCII: one column per byte, no need to
			// consult runewidth (asserted by TestClipFits...Assumption).
			start := i
			for i++; i < len(s) && s[i] >= 0x20 && s[i] < 0x7f; i++ {
			}
			col += i - start
			if col > width {
				return false
			}
			continue
		} else if c == 0x1b { // escape sequence: copied verbatim, uncounted
			j, ascii := escapeEnd(s, i)
			if !ascii && !utf8.ValidString(s[i:j]) {
				return false // would round-trip through U+FFFD
			}
			i = j
			continue
		} else if c < utf8.RuneSelf { // control char: rewritten to a space
			return false
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false // invalid UTF-8: the rewrite substitutes U+FFFD
		}
		col += runewidth.RuneWidth(r)
		if col > width {
			return false
		}
		i += size
	}
	return true
}

// escapeEnd returns the index just past the ANSI escape sequence starting at
// s[i] (assumed to be ESC) — everything up to and including the first ASCII
// letter, or the end of the string — and whether that span was pure ASCII
// (in which case the caller can skip the UTF-8 validity check, which is
// otherwise a quarter of the scan's cost on glamour-styled rows). Byte-wise
// scanning is equivalent to the rune-wise scan it replaces: a multi-byte
// rune's bytes are all >= 0x80 and so can never be mistaken for the
// terminating letter.
func escapeEnd(s string, i int) (int, bool) {
	ascii := true
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return j + 1, ascii
		}
		if c >= utf8.RuneSelf {
			ascii = false
		}
	}
	return len(s), ascii
}

// clipToWidthRewrite is the general path: it materializes the clipped row.
func clipToWidthRewrite(s string, width int) string {
	col := 0
	var b strings.Builder
	rs := []rune(s)
	clipped := false
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' { // copy the whole escape sequence, uncounted
			j := i + 1
			for j < len(rs) && !((rs[j] >= 'A' && rs[j] <= 'Z') || (rs[j] >= 'a' && rs[j] <= 'z')) {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(string(rs[i:j]))
			i = j
			continue
		}
		r := rs[i]
		if r < 0x20 || r == 0x7f { // control char → space (keeps the row one physical line)
			r = ' '
		}
		w := runewidth.RuneWidth(r)
		if col+w > width {
			clipped = true
			break
		}
		b.WriteRune(r)
		col += w
		i++
	}
	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func renderProseNode(n livedoc.Node, width int) []string {
	return render.Prose(n.Markdown, width)
}

// renderThinkingNode renders extended-thinking text as a dim blockquote
// (glamour styles "> " spans), visually distinct from the agent's prose.
func renderThinkingNode(n livedoc.Node, width int) []string {
	return render.Prose(blockquote(n.Markdown), width)
}

// renderSteeringNode renders a user message injected mid-turn — a steering
// interjection — under a marked gutter so it reads as the user's voice inside
// the assistant's turn, distinct from prose and thinking.
func renderSteeringNode(n livedoc.Node, width int) []string {
	rows := render.Prose(n.Markdown, width)
	return append([]string{term.Dim("↳ you")}, rows...)
}

// renderToolNode draws a tool as a widget with ZERO per-tool control flow:
// a status glyph, the tool name, and — when set — the producer's Summary
// (truncated for the header, wrapped in verbose mode); then any streamed
// output under a dim gutter, tail-clamped to bashCap lines. In verbose mode
// Args are also rendered generically as sorted key=value lines. The client
// never inspects n.Name.
func renderToolNode(n livedoc.Node, width, bashCap int, tick uint64, expand bool) []string {
	var glyph string
	switch n.Status {
	case livedoc.StatusOK:
		glyph = term.Green("✓")
	case livedoc.StatusError:
		glyph = term.Red("✗")
	default:
		frames := livedoc.SpinnerFrames
		glyph = term.Cyan(string(frames[int(tick)%len(frames)]))
	}
	name := n.Name
	if name == "" {
		name = "tool"
	}
	header := glyph + " " + term.Cyan(name)
	if n.Summary != "" {
		header = header + " " + term.Dim(truncCols(n.Summary, toolSummaryCap))
	}
	if n.StartedAt != 0 {
		header += " " + term.Dim("["+toolDuration(n, time.Now())+"]")
	}
	// Header, optional arg/timestamp lines, then the tail-clamped output.
	rows := make([]string, 1, 6+max(bashCap, 0))
	rows[0] = header

	if expand && len(n.Args) > 0 {
		const g = "  "
		keys := make([]string, 0, len(n.Args))
		for k := range n.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			line := fmt.Sprintf("%s=%v", k, n.Args[k])
			for _, l := range hardWrap(line, width-len(g)) {
				rows = append(rows, term.Dim(g+l))
			}
		}
	}
	if expand && n.StartedAt != 0 {
		rows = append(rows, term.Dim("  started "+formatToolTime(n.StartedAt)))
		if n.FinishedAt != 0 {
			rows = append(rows, term.Dim("  finished "+formatToolTime(n.FinishedAt)))
		}
	}

	if strings.TrimSpace(n.Output) != "" {
		// Tool stdout is the most likely vector for terminal-state
		// escapes that could break the painter (alt-screen, cursor
		// visibility, line wrap, mouse modes, OSC). Sanitize before
		// rendering so a wayward bubbletea / huh / less / etc. can
		// never bleed its escapes into the host terminal.
		output := strings.TrimRight(n.Output, "\n")
		safe := render.SanitizeForTerminal(output)
		shown, total := tailOutput(safe, bashCap)
		lines := strings.Split(shown, "\n")
		if bashCap >= 0 && total > bashCap {
			rows = append(rows, term.Dim(fmt.Sprintf("  │ … last %d of %d lines", bashCap, total)))
		}
		const gutter = "  │ "
		dimGutter := term.Dim(gutter) // hoisted: one styled gutter, not one per line
		for _, l := range lines {
			rows = append(rows, dimGutter+truncCols(l, width-len(gutter)))
		}
	}
	return rows
}

func tailOutput(output string, limit int) (string, int) {
	total := 1 + strings.Count(output, "\n")
	if limit < 0 || total <= limit {
		return output, total
	}
	if limit == 0 {
		return "", total
	}
	at := len(output)
	for range limit {
		at = strings.LastIndexByte(output[:at], '\n')
		if at < 0 {
			return output, total
		}
	}
	return output[at+1:], total
}

func toolDuration(n livedoc.Node, now time.Time) string {
	end := n.FinishedAt
	if end == 0 {
		end = now.UnixMilli()
	}
	d := time.Duration(end-n.StartedAt) * time.Millisecond
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func formatToolTime(ms int64) string {
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05.000 MST")
}

// blockquote prefixes each line of s with "> " (markdown blockquote).
func blockquote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// hardWrap char-wraps s (runewidth-aware) to at most w columns per line,
// preserving explicit newlines.
func hardWrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var rows []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			rows = append(rows, "")
			continue
		}
		col := 0
		var b strings.Builder
		for _, r := range para {
			rw := runewidth.RuneWidth(r)
			if col+rw > w {
				rows = append(rows, b.String())
				b.Reset()
				col = 0
			}
			b.WriteRune(r)
			col += rw
		}
		rows = append(rows, b.String())
	}
	return rows
}

// truncCols truncates s to at most w display columns (runewidth-aware).
func truncCols(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return runewidth.Truncate(s, w, "")
}
