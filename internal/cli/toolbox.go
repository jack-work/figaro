package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/partialjson"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// The tool box.
//
// A tool call is drawn as one framed object rather than a stack of rows that
// happen to be adjacent: a top edge that the header sits on, a left and right
// border down the body, a junction where the call stops and its result begins,
// and a bottom edge that closes once there is a result to close under.
//
//	⠋ write ─────────────────────────────────────────┐
//	  │                                              │
//	  │ content (…last 5 of 54 lines)                │
//	  │   37. Bartolo is consoled with the dowry.    │
//	  │                                              │
//	  │ path    /var/tmp/x/opera.md                  │
//	  │                                              │
//	✓ done [4ms] ────────────────────────────────────┤
//	  │                                              │
//	  │ Wrote 5453 bytes to /var/tmp/x/opera.md      │
//	  │                                              │
//	  └──────────────────────────────────────────────┘
//
// Two properties are load-bearing and easy to lose:
//
//   - The box is only as wide as its content, and never wider than the pane
//     less boxRightGap. A full-width box is indistinguishable from the turn
//     rule above it, which is genuinely full width — they clashed.
//   - Every row is exactly boxWidth cells. The painter counts rows and columns;
//     a row that is one cell wide of its neighbours puts the right border on a
//     ragged edge and, at the pane's last column, wraps.
const (
	// boxIndent is how far the left border sits from the margin, and
	// boxPadLeft the air between that border and the content.
	boxIndent  = "  "
	boxPadLeft = " "
	// boxPadRight is the air between content and the right border. TWO
	// columns, at the owner's request: one column reads as a clipped row
	// rather than as a frame, and the box must be visibly narrower than the
	// full-width turn rule so the two are not confused.
	boxPadRight = "  "
	// boxRightGap keeps the whole box clear of the pane's last column. A box
	// drawn flush to the edge has nowhere to put its border when a wide rune
	// lands on the boundary.
	boxRightGap = 1
	// boxMinContent is the narrowest content column worth drawing. Below it
	// the frame costs more than it explains and the block falls back to plain
	// gutter rows.
	boxMinContent = 20
)

// box is the geometry of one tool block: everything the row builders need to
// agree on, computed once from the pane width and the content.
type box struct {
	content int // columns available to content inside the borders
	total   int // columns from the margin to the right border, inclusive
}

// newBox fits a box to the widest content row it will have to hold, bounded by
// the pane. widest is measured in cells, and may be zero.
func newBox(width, widest int) box {
	frame := len(boxIndent) + 1 + len(boxPadLeft) + len(boxPadRight) + 1
	max := width - boxRightGap - frame
	if max < boxMinContent {
		max = boxMinContent
	}
	c := widest
	if c > max {
		c = max
	}
	if c < boxMinContent {
		c = boxMinContent
	}
	return box{content: c, total: frame + c}
}

// fits reports whether the pane can hold the box at all. When it cannot, the
// caller draws the old ungated gutter rows: a frame that does not close is
// worse than no frame.
func (b box) fits(width int) bool { return b.total <= width-boxRightGap }

// row draws one body row: left border, content padded to the content column,
// right border. content may carry ANSI; padding is measured in cells.
func (b box) row(content string) string {
	// CLIP as well as pad. Every row in the box comes through here, so this is
	// the one place that can guarantee the right border lands in the same
	// column on every row — and the rows that overflow are never the ones you
	// predict (a fold note at width 20, a timestamp at width 28).
	if term.VisibleLen(content) > b.content {
		content = clipToWidthEllipsis(content, b.content)
	}
	pad := b.content - term.VisibleLen(content)
	if pad < 0 {
		pad = 0
	}
	return term.Dim(boxIndent+"│"+boxPadLeft) + content + strings.Repeat(" ", pad) +
		term.Dim(boxPadRight+"│")
}

// blank is the air row that opens and closes each section of the box.
func (b box) blank() string { return b.row("") }

// top draws the header: the label, then the edge out to the corner. The label
// is NOT inside the border — it sits on the edge, the way a fieldset legend
// does, so the eye reads it as naming the box rather than as its first row.
func (b box) top(label string) string {
	return b.edge(label, "┐")
}

// junction draws the divider between the call and its result, labelled the
// same way as the top.
func (b box) junction(label string) string {
	return b.edge(label, "┤")
}

// bottom closes the box.
func (b box) bottom() string {
	return term.Dim(boxIndent + "└" + strings.Repeat("─", b.total-len(boxIndent)-2) + "┘")
}

// edge draws a labelled horizontal edge ending in corner. A label longer than
// the box is clipped to it: the corner is not optional, because an unclosed
// frame reads as a rendering fault.
func (b box) edge(label, corner string) string {
	room := b.total - 1
	if term.VisibleLen(label) > room {
		label = clipToWidth(label, room)
	}
	fill := room - term.VisibleLen(label)
	if fill < 0 {
		fill = 0
	}
	return label + term.Dim(strings.Repeat("─", fill)+corner)
}

// boxLines renders one value into body rows: WRAPPED when expanded, and
// ELLIPSISED to a single row when not. The fold is the whole difference
// between the two states — a folded box says what is there, an expanded one
// shows it — and it is why a folded row never needs a wrap.
func boxLines(value string, width int, expand bool) []string {
	if expand {
		return hardWrap(value, width)
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(value, "\n") {
		out = append(out, clipToWidthEllipsis(line, width))
	}
	return out
}

// toolStatusLabel is the junction's label: what the EXECUTION is doing, as
// distinct from what the header names, which is the call.
//
// The duration on it is the execution's. When the generation clock lands (the
// agent knows when the tool block opened; it does not publish it yet) the
// header takes that number and this one stays the runtime, which is the split
// the owner drew.
func toolStatusLabel(glyph, status string, dur string) string {
	word := "done"
	switch status {
	case "running":
		word = "running"
	case "error":
		word = "failed"
	}
	label := glyph + " " + term.Label(word)
	if dur != "" {
		label += " " + term.Dim("["+dur+"]")
	}
	return label + " "
}

// widestCell is the widest of a set of already-rendered contents, in cells.
func widestCell(rows ...[]string) int {
	w := 0
	for _, set := range rows {
		for _, r := range set {
			if n := term.VisibleLen(r); n > w {
				w = n
			}
		}
	}
	return w
}

// argStreamLines / argSettledLines are the folds on ONE argument's value.
// Streaming keeps the LAST rows — a moving window on what is being typed;
// settled keeps the FIRST — what the call is, rather than where it stopped.
const (
	argStreamLines  = 5
	argSettledLines = 2
	// argLabelCap bounds the shared label column so one long argument name
	// cannot push every value off the right of the box.
	argLabelCap = 12
)

// toolArgRows draws a tool's arguments as box content: one label per field,
// its value beside it when it is short and beneath it when it is not. The same
// shape whether the value is still arriving (Input, a truncated JSON prefix
// walked by partialjson) or settled (Args, decoded) — a node must not change
// layout under the reader because the model finished typing.
//
// A folded multi-line value names its fold ON THE LABEL — `content (…last 5 of
// 41 lines)` — so the fold costs no row. Expanded, the note is gone and the
// value is wrapped instead: there is nothing to count when everything is
// shown.
func toolArgRows(n livedoc.Node, content int, expand bool) []string {
	fields := toolArgFields(n)
	if len(fields) == 0 {
		return nil
	}
	streaming := strings.TrimSpace(n.Input) != ""
	label := 0
	for _, f := range fields {
		if w := runewidth.StringWidth(f.Name); w > label && w <= argLabelCap {
			label = w
		}
	}
	var rows []string
	for _, f := range fields {
		value := strings.TrimRight(render.SanitizeForTerminal(f.Value), "\n")
		// A short one-line value rides beside its label; anything longer gets
		// the label to itself and the value indented beneath, where a wrapped
		// remainder cannot be read as the next argument.
		if !strings.Contains(value, "\n") && runewidth.StringWidth(value) <= content-label-1 {
			row := term.Label(runewidth.FillRight(f.Name, label))
			if value != "" {
				row += " " + term.Arg(value)
			}
			rows = append(rows, row)
			continue
		}
		lines := boxLines(value, content-2, expand)
		kept := foldArgLines(lines, expand, streaming)
		head := f.Name
		if len(kept) < len(lines) {
			head += " " + argFoldNote(len(kept), len(lines), streaming)
		}
		rows = append(rows, term.Label(head))
		for _, l := range kept {
			rows = append(rows, "  "+term.Arg(l))
		}
		// A multi-line value closes with air, so the next argument starts
		// clear of it. A single-line value does not: nothing was separated.
		rows = append(rows, "")
	}
	// The section's own air is drawn by the box; a trailing blank here would
	// double it.
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// foldArgLines applies the fold: the LAST argStreamLines rows while the value
// is arriving, the FIRST argSettledLines (blank rows skipped) once it has
// stopped, everything when expanded.
func foldArgLines(lines []string, expand, streaming bool) []string {
	if expand {
		return lines
	}
	switch {
	case streaming && len(lines) > argStreamLines:
		return lines[len(lines)-argStreamLines:]
	case !streaming:
		kept := make([]string, 0, argSettledLines)
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if kept = append(kept, l); len(kept) == argSettledLines {
				break
			}
		}
		return kept
	}
	return lines
}

// argFoldNote says which END was kept, because that differs by phase and a
// reader cannot otherwise tell whether they are looking at the beginning of a
// command or the end of one.
func argFoldNote(shown, total int, streaming bool) string {
	end := "first"
	if streaming {
		end = "last"
	}
	return fmt.Sprintf("(…%s %d of %d lines)", end, shown, total)
}

// toolArgFields answers "what are this tool's arguments" — the streaming
// prefix while it arrives, the decoded map once it lands — sorted by name in
// BOTH phases, since the streamed order is the model's and the settled order
// is a Go map's, and anything else reshuffles the block the instant the
// arguments land.
func toolArgFields(n livedoc.Node) []partialjson.Field {
	if strings.TrimSpace(n.Input) != "" {
		f := partialjson.Fields([]byte(n.Input))
		sort.Slice(f, func(i, j int) bool { return f[i].Name < f[j].Name })
		return f
	}
	if len(n.Args) == 0 {
		return nil
	}
	keys := make([]string, 0, len(n.Args))
	for k := range n.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]partialjson.Field, 0, len(keys))
	for _, k := range keys {
		v, ok := n.Args[k].(string)
		if !ok {
			v = fmt.Sprintf("%v", n.Args[k])
		}
		out = append(out, partialjson.Field{Name: k, Value: v, Done: true})
	}
	return out
}

// toolMetaRows is the call's metadata: what Ctrl-O shows and what a selection
// shows, and deliberately the ONLY thing Ctrl-O adds. Verbosity is metadata,
// not content.
func toolMetaRows(n livedoc.Node, show bool) []string {
	if !show || n.StartedAt == 0 {
		return nil
	}
	rows := []string{term.Label("started ") + term.Arg(formatToolTime(n.StartedAt))}
	if n.FinishedAt != 0 {
		rows = append(rows, term.Label("finished ")+term.Arg(formatToolTime(n.FinishedAt)))
	}
	return rows
}

// toolDurationOrEmpty is the execution duration, or "" when the tool has not
// started running yet.
func toolDurationOrEmpty(n livedoc.Node) string {
	if n.StartedAt == 0 {
		return ""
	}
	return toolDuration(n, time.Now())
}
