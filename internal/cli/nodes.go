package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

const (
	nodeBashCapDefault = 10
	toolSummaryCap     = 80 // default truncation for a tool's summary line
)

// renderSettings is the consumer-side verbosity toggle. The wire/IR always
// carries the full data; this only affects display, so it can be flipped live
// (Ctrl-O) and the unit re-rendered. Thinking blocks are always shown (muted);
// verbose additionally expands tool inputs to the full wrapped command.
type renderSettings struct {
	verbose  bool
	jsonMode bool // -j / --json: emit a single {aria_id, ...} JSON line on stdout instead of a live render
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
		var nr []string
		switch n.Type {
		case livedoc.NodeTool:
			nr = renderToolNode(n, width, bashCap, tick, set.verbose)
		case livedoc.NodeThinking:
			nr = renderThinkingNode(n, width)
		case livedoc.NodeSteering:
			nr = renderSteeringNode(n, width)
		default:
			nr = renderProseNode(n, width)
		}
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
func clipToWidth(s string, width int) string {
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

// scrolledGlyph marks a tool header that the viewport-overflow flush committed
// to scrollback while the tool was still running. Scrollback is immutable, so
// the tool's eventual ✓/✗ can never land there; freezing the live spinner frame
// would leave a half-drawn animation stuck in history forever.
const scrolledGlyph = '◦'

// stabilizeForScrollback rewrites a row about to be frozen into immutable
// scrollback so it carries no animated state. Today that means replacing a
// leading spinner frame (a running tool's header glyph) with a static marker:
// when many parallel tools overflow the viewport, the top ones get flushed
// before they finish, and an animated frame would otherwise stick in history.
// Rows whose first visible glyph isn't a spinner frame pass through unchanged,
// so it is safe to apply to every flushed row (final tool headers carry ✓/✗,
// prose carries text).
func stabilizeForScrollback(row string) string {
	rs := []rune(row)
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' { // skip an escape sequence; the glyph sits after the color codes
			j := i + 1
			for j < len(rs) && !((rs[j] >= 'A' && rs[j] <= 'Z') || (rs[j] >= 'a' && rs[j] <= 'z')) {
				j++
			}
			if j < len(rs) {
				j++
			}
			i = j
			continue
		}
		if isSpinnerFrame(rs[i]) { // the first visible glyph is an animated spinner
			rs[i] = scrolledGlyph
		}
		break // only the leading glyph matters
	}
	return string(rs)
}

func isSpinnerFrame(r rune) bool {
	for _, f := range livedoc.SpinnerFrames {
		if f == r {
			return true
		}
	}
	return false
}

// nodesRunning reports whether any tool node is still running (so the
// caller animates the spinner).
func nodesRunning(nodes []livedoc.Node) bool {
	for _, n := range nodes {
		if n.Type == livedoc.NodeTool && n.Status == livedoc.StatusRunning {
			return true
		}
	}
	return false
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
	rows := []string{header}

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

	if strings.TrimSpace(n.Output) != "" {
		// Tool stdout is the most likely vector for terminal-state
		// escapes that could break the painter (alt-screen, cursor
		// visibility, line wrap, mouse modes, OSC). Sanitize before
		// rendering so a wayward bubbletea / huh / less / etc. can
		// never bleed its escapes into the host terminal.
		safe := render.SanitizeForTerminal(strings.TrimRight(n.Output, "\n"))
		lines := strings.Split(safe, "\n")
		if total := len(lines); total > bashCap {
			lines = lines[total-bashCap:]
			rows = append(rows, term.Dim(fmt.Sprintf("  │ … last %d of %d lines", bashCap, total)))
		}
		const gutter = "  │ "
		for _, l := range lines {
			rows = append(rows, term.Dim(gutter)+truncCols(l, width-len(gutter)))
		}
	}
	return rows
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
