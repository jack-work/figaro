package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/config"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

const (
	nodeBashCapDefault  = 10
	nodeOutputUnlimited = -1
	toolSummaryCap      = 80
)

type renderSettings struct {
	more        bool
	details     bool
	coordFormat string
	sticky      bool
	jsonMode    bool
	listen      bool
	record      string // wire tape path; empty opens nothing
}

// renderNode is the one dispatch on node kind shared by every view.
func renderNode(n livedoc.Node, width, bashCap int, tick uint64, expanded bool) []string {
	switch {
	case n.Type == livedoc.NodeTool:
		return renderToolNode(n, width, bashCap, tick, expanded)
	case n.Type == livedoc.NodeThinking:
		return renderThinkingNode(n, width)
	case n.Type == livedoc.NodeSteering:
		return renderSteeringNode(n, width)
	default:
		return renderProseNode(n, width)
	}
}

// nodeExpandable reports whether toggling would reveal anything; only a tool has a
// second form.
func nodeExpandable(n livedoc.Node) bool {
	if n.Type != livedoc.NodeTool {
		return false
	}
	// Arguments count: a tool still streaming them has no output yet.
	return strings.TrimSpace(n.Output) != "" ||
		strings.TrimSpace(n.Input) != "" || len(n.Args) > 0
}

func renderTurnRows(m aria.Message, width int, tick uint64, set renderSettings) []string {
	return ldrender.Painted(turnComposer(m, width, tick, set).Message(m, width), width, set.more)
}

func renderNodeList(nodes []livedoc.Node, width int, tick uint64, set renderSettings) []string {
	return ldrender.Painted(turnComposer(aria.Message{Nodes: nodes}, width, tick, set).Nodes(nodes, width), width, set.more)
}

func turnComposer(m aria.Message, width int, tick uint64, set renderSettings) ldrender.Composer {
	turn := m.Turn
	c := ldrender.Composer{
		View:        &ariaView{settings: &set},
		Header:      messageHeader,
		Rule:        func() string { return dimTransRule(width) },
		InputHeader: inputHeader,
		HeadMore:    headMore,
		Tick:        int(tick),
		Adorn: func(block int, n livedoc.Node, deltas map[string]livedoc.FormDelta, w int) ldrender.Adornment {
			sec := metaSection{
				ref:    blockRef(m, block),
				block:  block,
				deltas: deltas,
				open:   set.details && len(deltas) > 0,
			}
			if sec.open {
				held := m
				sec.src, sec.m = metaSource{layout: set.coordFormat}, &held
			}
			return buildAdornment(adornerFor(block, n), sec, w)
		},
		Quote: func(q quote.Mention, w int, _ bool) []string { return quoteRows(q, w, true) },
		Mark: func(block int, n livedoc.Node) string {
			return term.Dim(coordLabel(turn, block, nodeCoordAt(n), set.coordFormat))
		},
	}
	return c
}

// proseIndent matches the inset render.Prose gives a paragraph.
const proseIndent = "  "

// clipToWidth passes escapes through uncounted, appends a reset after a cut, and
// flattens control characters to spaces: a row must be exactly one physical line
// or the painter's cursor math desyncs.
func clipToWidth(s string, width int) string {
	if clipFits(s, width) {
		return s
	}
	return clipToWidthRewrite(s, width)
}

// clipFits reports whether clipToWidthRewrite(s, width) == s, without allocating.
func clipFits(s string, width int) bool {
	col := 0
	for i := 0; i < len(s); {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			start := i
			for i++; i < len(s) && s[i] >= 0x20 && s[i] < 0x7f; i++ {
			}
			col += i - start
			if col > width {
				return false
			}
			continue
		} else if c == 0x1b {
			j, ascii := escapeEnd(s, i)
			if !ascii && !utf8.ValidString(s[i:j]) {
				return false
			}
			i = j
			continue
		} else if c < utf8.RuneSelf {
			return false
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}
		col += runewidth.RuneWidth(r)
		if col > width {
			return false
		}
		i += size
	}
	return true
}

// escapeEnd returns the index past the escape at s[i] and whether the span was
// pure ASCII, which lets the caller skip the UTF-8 check. The grammar lives in
// render.SkipEscape only.
func escapeEnd(s string, i int) (int, bool) {
	end := render.SkipEscape(s, i)
	ascii := true
	for j := i; j < end; j++ {
		if s[j] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	return end, ascii
}

// displayWidth excludes escapes; runewidth.StringWidth would count an SGR run as
// characters.
func displayWidth(s string) int {
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j, _ := escapeEnd(s, i)
			i = j
			continue
		}
		if c := s[i]; c >= 0x20 && c < 0x7f {
			col++
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			break
		}
		if r >= 0x20 {
			col += runewidth.RuneWidth(r)
		}
		i += size
	}
	return col
}

// displayWidthBytes is displayWidth without the string conversion the paint path
// may not allocate.
func displayWidthBytes(b []byte) int { return displayWidthBytesUpTo(b, -1) }

// displayWidthBytesUpTo returns stop once reached; a negative stop counts the
// whole buffer.
func displayWidthBytesUpTo(b []byte, stop int) int {
	col := 0
	for i := 0; i < len(b); {
		if stop >= 0 && col >= stop {
			return stop
		}
		if b[i] == 0x1b {
			i = render.SkipEscapeBytes(b, i)
			continue
		}
		if c := b[i]; c >= 0x20 && c < 0x7f {
			col++
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if size == 0 {
			break
		}
		if r >= 0x20 {
			col += runewidth.RuneWidth(r)
		}
		i += size
	}
	// A double-width glyph can straddle the stop; the contract is at most stop.
	if stop >= 0 && col > stop {
		return stop
	}
	return col
}

// clipToWidthEllipsis spends one column on an ellipsis when it cuts.
func clipToWidthEllipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width && clipFits(s, width) {
		return s
	}
	if body, clipped := clipRewrite(s, width); !clipped {
		return body
	}
	if width == 1 {
		return "…\x1b[0m"
	}
	body, _ := clipRewrite(s, width-1)
	body = strings.TrimSuffix(body, "\x1b[0m")
	return body + "…\x1b[0m"
}

func clipToWidthRewrite(s string, width int) string {
	out, _ := clipRewrite(s, width)
	return out
}

func clipRewrite(s string, width int) (string, bool) {
	col := 0
	var b strings.Builder
	clipped := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := render.SkipEscape(s, i)
			seq := s[i:j]
			if !utf8.ValidString(seq) {
				// Never put invalid UTF-8 on the wire.
				seq = string([]rune(seq))
			}
			b.WriteString(seq)
			i = j
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		w := runewidth.RuneWidth(r)
		if col+w > width {
			clipped = true
			break
		}
		b.WriteRune(r)
		col += w
		i += sz
	}

	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String(), clipped
}

func renderProseNode(n livedoc.Node, width int) []string {
	return nodeProseRows(n, width)
}

func renderThinkingNode(n livedoc.Node, width int) []string {
	return nodeProseRows(n, width)
}

func renderSteeringNode(n livedoc.Node, width int) []string {
	head, _ := steeringHead(n)
	return append([]string{head}, nodeProseRows(n, width)...)
}

func nodeMarkdown(n livedoc.Node) string { return n.Markdown }

// quoteGutter is drawn on every rendered row; glamour prefixes markdown lines, not
// rows.
const quoteGutter = "  │ "

func quoted(t livedoc.NodeType) bool {
	return t == livedoc.NodeThinking || t == livedoc.NodeSteering
}

// proseWidth is shared by nodeExpandable and the renderers, or the predicate lies.
func proseWidth(n livedoc.Node, width int) int {
	if w := width - quoteGutterCells; quoted(n.Type) && w > 0 {
		return w
	}
	return width
}

// quoteGutterCells is columns, not bytes: the rule is a three-byte rune.
const quoteGutterCells = 4

func nodeProseRows(n livedoc.Node, width int) []string {
	rows := render.Prose(nodeMarkdown(n), proseWidth(n, width))
	if !quoted(n.Type) {
		return rows
	}
	dim := term.Dim(quoteGutter)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		// The rule stands in glamour's paragraph margin rather than on top
		// of it.
		out = append(out, dim+dedentProse(r))
	}
	return out
}

// dedentProse removes one proseIndent, looking past the SGR runs before the first
// visible column.
func dedentProse(row string) string {
	var keep strings.Builder
	i, dropped := 0, 0
	for i < len(row) && dropped < len(proseIndent) {
		if row[i] == 0x1b {
			j, _ := escapeEnd(row, i)
			keep.WriteString(row[i:j])
			i = j
			continue
		}
		if row[i] != ' ' {
			break
		}
		i++
		dropped++
	}
	if dropped < len(proseIndent) {
		return row
	}
	return keep.String() + row[i:]
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

// timeNow is indirected so the golden can freeze a running call's elapsed time.
var timeNow = time.Now

// toolElapsed counts from the model opening the call, not from the tool starting.
func toolElapsed(n livedoc.Node) string {
	from := n.OpenedAt
	if from == 0 {
		from = n.StartedAt
	}
	if from == 0 {
		return ""
	}
	to := n.FinishedAt
	if to == 0 {
		to = timeNow().UnixMilli()
	}
	return formatDuration(to - from)
}

func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
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

// timeLocation is indirected so the golden can pin the zone.
var timeLocation = func() *time.Location { return time.Local }

func formatToolTime(ms int64) string {
	return time.UnixMilli(ms).In(timeLocation()).Format("2006-01-02 15:04:05.000 MST")
}

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

func truncCols(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return runewidth.Truncate(s, w, "")
}

// pagerSettings is the one place config-derived render settings are born; flags
// are laid on top by the caller.
func pagerSettings(loaded *config.Loaded) renderSettings {
	return renderSettings{
		coordFormat: loaded.CoordFormat(),
		sticky:      true,
	}
}
