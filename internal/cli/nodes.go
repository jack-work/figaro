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
	toolArgCap         = 80 // default truncation for a tool's arg summary
)

// renderSettings is the consumer-side verbosity toggle. The wire/IR always
// carries the full data; this only affects display, so it can be flipped live
// (Ctrl-O) and the unit re-rendered. Thinking blocks are always shown (muted);
// verbose additionally expands tool inputs to the full wrapped command.
type renderSettings struct {
	verbose bool
}

// renderNodes renders a unit's node list to terminal rows plus the
// stable-row watermark — the rows belonging to leading nodes that are
// final (a completed tool, or a block followed by a later node) and will
// not change again this unit. Each returned row fits within width so the
// painter's one-row-per-line cursor math holds.
func renderNodes(nodes []livedoc.Node, width, bashCap int, tick uint64, set renderSettings) ([]string, int) {
	if width <= 0 {
		width = 80
	}
	if bashCap <= 0 {
		bashCap = nodeBashCapDefault
	}
	firstLive := liveNodeIndex(nodes)
	var rows []string
	stable, emitted := 0, 0
	for i, n := range nodes {
		var nr []string
		switch n.Type {
		case livedoc.NodeTool:
			nr = renderToolNode(n, width, bashCap, tick, set.verbose)
		case livedoc.NodeThinking:
			nr = renderThinkingNode(n, width)
		default:
			nr = renderProseNode(n, width)
		}
		// One blank line between any two adjacent (visible) blocks. The
		// first emitted block sits flush to the top.
		if emitted > 0 {
			nr = append([]string{""}, nr...)
		}
		if i < firstLive {
			stable += len(nr)
		}
		rows = append(rows, nr...)
		emitted++
	}
	if stable > len(rows) {
		stable = len(rows)
	}
	// Guarantee every row fits the viewport: a row wider than width would
	// wrap onto a second physical line and desync the painter's
	// one-row-per-line cursor math. (The live session also disables
	// auto-wrap; this keeps the invariant even there and for static views.)
	for i := range rows {
		rows[i] = clipToWidth(rows[i], width)
	}
	return rows, stable
}

// clipToWidth truncates a styled row to at most width display columns,
// passing ANSI escape sequences through uncounted and appending a reset so
// a cut mid-color doesn't bleed.
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
		w := runewidth.RuneWidth(rs[i])
		if col+w > width {
			clipped = true
			break
		}
		b.WriteRune(rs[i])
		col += w
		i++
	}
	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// liveNodeIndex returns the index of the first node still mutating (a
// running tool, or the last node — the active tail). Everything before it
// is final. Returns len(nodes) when all are final.
func liveNodeIndex(nodes []livedoc.Node) int {
	for i, n := range nodes {
		final := true
		if n.Type == livedoc.NodeTool {
			final = n.Status != livedoc.StatusRunning
		} else {
			final = i < len(nodes)-1
		}
		if !final {
			return i
		}
	}
	return len(nodes)
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

// renderToolNode draws a tool as a widget: a status header (animated
// spinner while running, ✓/✗ when done) with the tool name and an argument
// summary, then the streamed output under a dim gutter, tail-clamped to
// bashCap lines. With expand, the full arguments are shown wrapped beneath
// the header instead of a truncated one-liner.
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
	detail := toolArgSummary(n)
	rows := []string{header}

	if detail != "" {
		const g = "  "
		if expand {
			// Full arguments, wrapped under the header.
			for _, l := range hardWrap(detail, width-len(g)) {
				rows = append(rows, term.Dim(g+l))
			}
		} else {
			rows[0] = header + " " + term.Dim(truncCols(detail, toolArgCap))
		}
	}

	if strings.TrimSpace(n.Output) != "" {
		lines := strings.Split(strings.TrimRight(n.Output, "\n"), "\n")
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

// toolArgSummary renders a tool's arguments as a one-line string: the
// command for bash, the path for file tools, else compact key=value pairs.
func toolArgSummary(n livedoc.Node) string {
	if n.Args == nil {
		return ""
	}
	switch n.Name {
	case "bash":
		if c, ok := n.Args["command"].(string); ok {
			return c
		}
	case "read", "write", "edit":
		if p, ok := n.Args["path"].(string); ok {
			return p
		}
	}
	keys := make([]string, 0, len(n.Args))
	for k := range n.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, n.Args[k]))
	}
	return strings.Join(parts, " ")
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
