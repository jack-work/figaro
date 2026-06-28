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

// renderNodes renders a unit's whole node list to terminal rows plus the
// stable-row watermark. Static views (aria) and tests use this; the live
// painter uses renderNodesFrom to render only the unflushed tail.
func renderNodes(nodes []livedoc.Node, width, bashCap int, tick uint64, set renderSettings) ([]string, int) {
	rows, stableRows, _ := renderNodesFrom(nodes, 0, false, width, bashCap, tick, set)
	return rows, stableRows
}

// renderNodesFrom renders nodes[start:] to terminal rows. With leadBlank it
// prepends a single blank separator (the inter-block spacing that separates
// the first live node from flushed content above). It reports the rows AND
// the node count of the leading FINAL prefix of the slice (final = a
// completed tool, or a block followed by a later node) plus that prefix's
// row count. Finality is judged against the WHOLE list, so a node's status
// is the same whether rendered here or as part of the full render. Each
// returned row fits within width so the painter's one-row-per-line cursor
// math holds.
func renderNodesFrom(nodes []livedoc.Node, start int, leadBlank bool, width, bashCap int, tick uint64, set renderSettings) (rows []string, stableRows, stableNodes int) {
	if width <= 0 {
		width = 80
	}
	if bashCap <= 0 {
		bashCap = nodeBashCapDefault
	}
	if start < 0 {
		start = 0
	}
	firstLive := liveNodeIndex(nodes)
	wantLead := leadBlank && start > 0 && start < len(nodes)
	emitted := 0
	for i := start; i < len(nodes); i++ {
		n := nodes[i]
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
		// first emitted block sits flush to the top — except a leading
		// separator (wantLead) ties it to the flushed content above; that
		// blank belongs to this node and flushes with it.
		if emitted > 0 || (emitted == 0 && wantLead) {
			nr = append([]string{""}, nr...)
		}
		if i < firstLive {
			stableRows += len(nr)
			stableNodes = i - start + 1
		}
		rows = append(rows, nr...)
		emitted++
	}
	if stableRows > len(rows) {
		stableRows = len(rows)
	}
	// Guarantee every row fits the viewport: a row wider than width would
	// wrap onto a second physical line and desync the painter's
	// one-row-per-line cursor math. (The live session also disables
	// auto-wrap; this keeps the invariant even there and for static views.)
	for i := range rows {
		rows[i] = clipToWidth(rows[i], width)
	}
	return rows, stableRows, stableNodes
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

// renderSteeringNode renders a user message injected mid-turn — a steering
// interjection — under a marked gutter so it reads as the user's voice inside
// the assistant's turn, distinct from prose and thinking.
func renderSteeringNode(n livedoc.Node, width int) []string {
	rows := render.Prose(n.Markdown, width)
	return append([]string{term.Dim("↳ you")}, rows...)
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
