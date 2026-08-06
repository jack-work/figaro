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
	// boxPadRight is the air kept to the right of content. There is no right
	// border to protect any more, but the block must stay visibly narrower
	// than the full-width turn rule so the two are not confused.
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

// newBox fits a block to the widest content row it will have to hold, bounded
// by the pane. widest is measured in cells, and may be zero.
func newBox(width, widest int) box {
	frame := len(boxIndent) + 1 + len(boxPadLeft) + len(boxPadRight)
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

// row draws one body row: the left rule, then the content. There is NO right
// border and no floor — a fully closed box made the left of the screen too
// busy, and the frame that earns its keep is the one the eye follows down.
//
// It still CLIPS. Every row in the block comes through here, so this is the
// one place that can guarantee no row outruns its pane, and the rows that
// overflow are never the ones you predict (a fold note at width 20, a
// timestamp at width 28).
func (b box) row(content string) string {
	if term.VisibleLen(content) > b.content {
		content = clipToWidthEllipsis(content, b.content)
	}
	return term.Dim(boxIndent+"│"+boxPadLeft) + content
}

// blank is the air row that opens and closes each section of the box.
func (b box) blank() string { return b.row("") }

// elbow closes a finished block: a stub, not a floor. It says "this call ends
// here" without drawing a rule across the screen, which is what made the
// transcript look ruled rather than written.
func (b box) elbow() string { return term.Dim(boxIndent + "└──") }

// top and junction are the block's two labels: what the CALL is, and what its
// EXECUTION is doing. They carry no rule — a horizontal bar across the screen
// for every tool call made the transcript look ruled rather than written, and
// the left gutter already says where the block starts and stops.
func (b box) top(label string) string      { return clipToWidth(label, b.total) }
func (b box) junction(label string) string { return clipToWidth(label, b.total) }

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
// distinct from what the header names, which is the call. Its duration is the
// RUNTIME; the header's is the GENERATION. Two clocks, because for a large
// write they differ by thirty seconds and only one of them used to exist.
func toolStatusLabel(glyph, status string, dur string) string {
	word := "done"
	switch status {
	case "running":
		word = "running"
	case "error":
		word = "failed"
	}
	// The status word is the tool name's colour: they are the two things that
	// NAME this block, one at each end of it.
	label := glyph + " " + term.Arg(word)
	if dur != "" {
		label += " " + term.Dim("["+dur+"]")
	}
	return label
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
		// A ONE-LINE value rides beside its label however long it is, cut with
		// an ellipsis when it does not fit. Dropping a long command onto its
		// own row below the label made a single argument look like two, and
		// the label column then said nothing at all.
		//
		// Expanded is the exception: there the value is meant to be READ, so
		// one that cannot fit takes the label to itself and wraps beneath it.
		oneLine := !strings.Contains(value, "\n")
		room := content - label - 1
		if oneLine && (!expand || runewidth.StringWidth(value) <= room) {
			row := term.Label(runewidth.FillRight(f.Name, label))
			if value != "" {
				row += " " + term.Arg(clipToWidthEllipsis(value, room))
			}
			rows = append(rows, row)
			continue
		}
		lines := boxLines(value, content-2, expand)
		kept := foldArgLines(lines, expand, streaming)
		head := f.Name
		if len(kept) < len(lines) {
			head += " " + argFoldNote(len(kept), len(lines))
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

// argFoldNote is the count on a folded value's label: `(2 of 41 lines)`. Which
// END was kept is not spelled out — streaming keeps the tail, settled keeps the
// head — because the value is right there to read and the words cost more than
// they explain.
func argFoldNote(shown, total int) string {
	return fmt.Sprintf("(%d of %d lines)", shown, total)
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

// toolRuntime is how long the call has been RUNNING: started to finished, or
// to now while it still is. Empty before it starts.
func toolRuntime(n livedoc.Node) string {
	if n.StartedAt == 0 {
		return ""
	}
	end := n.FinishedAt
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	return formatDuration(end - n.StartedAt)
}

// toolGeneration is how long the MODEL spent writing the call: the block
// opening to the moment it began running, or to now while it is still being
// written.
//
// Empty when the aria predates the opened_at clock — every tool node already
// on disk. Those fall back to the runtime in the header, which is what they
// have always shown, rather than to a blank.
func toolGeneration(n livedoc.Node) string {
	if n.OpenedAt == 0 {
		return ""
	}
	end := n.StartedAt
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	return formatDuration(end - n.OpenedAt)
}
