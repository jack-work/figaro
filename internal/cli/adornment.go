package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

// THE ADORNMENT: a block's form deltas drawn as part of the block.
//
// Collapsed, a block wears one glyph in the right gutter (Δ) and nothing
// else. Open, every delta is a row of its own -- a pseudonode, selected and
// yanked like a node -- and a snake joins the rows to the block they
// explain: its head (Δ) marks the selected row, and parks at the anchor
// when the selection is elsewhere. The MORE a reader asks for, the more
// rows: a block that has state says so in one column until it is asked.
//
// Three layouts, one law. A tool hangs the snake off a connector row under
// its output; prose off the first line of its own text; a turn's question
// ENCLOSES its deltas between the text and the rule that closes the seam,
// so its snake runs down to a corner instead of up to a block. Everything
// else -- the glyph law, the row bodies, the refs the rows take -- is
// shared, which is what makes the three one primitive rather than three.

// The snake's glyphs. One column each, and painted plain: the row they
// stand in carries the rendition, so a glyph is never a second colour.
const (
	snakeHang  = "╭" // the anchor, while the cursor is in the list below it
	snakeBody  = "│" // between the anchor and the cursor
	snakeClose = "╰" // the corner an enclosed adornment closes on
	snakeElbow = "╯" // a tool connector's turn back into its own gutter
)

// adorner is one node type's layout. The methods are the whole of what
// differs between a tool, a line of prose and a turn's question.
type adorner interface {
	// spineCol is the column the snake takes, left of the rows.
	spineCol() int
	// textCol is where a delta row's text begins.
	textCol() int
	// anchorRow is a row of the adornment's OWN to hang the snake from,
	// and false when it hangs from the block's first row instead.
	anchorRow() (string, bool)
	// tail closes an adornment the chrome beneath it encloses, drawn into
	// that chrome's first column.
	tail() string
	// liftFork reports whether a fork leaves the list and rides the
	// block's chrome. True on the question, where a fork happens; see
	// deltaRows.
	liftFork() bool
	// gutterLast puts the collapsed marker on the block's LAST row, which
	// is where prose tacks it: onto the end of what it says.
	gutterLast() bool
}

// proseAdorner hangs the snake off the first line of the prose it adorns:
// there is no chrome to hang it from, and the text's own left margin is
// free.
type proseAdorner struct{}

func (proseAdorner) spineCol() int             { return 0 }
func (proseAdorner) textCol() int              { return 2 }
func (proseAdorner) anchorRow() (string, bool) { return "", false }
func (proseAdorner) tail() string              { return "" }
func (proseAdorner) liftFork() bool            { return false }
func (proseAdorner) gutterLast() bool          { return true }

// toolAdorner hangs the snake off a connector row below the tool's output,
// whose elbow turns out of the output gutter the widget already draws.
type toolAdorner struct{}

func (toolAdorner) spineCol() int             { return 1 }
func (toolAdorner) textCol() int              { return 3 }
func (toolAdorner) anchorRow() (string, bool) { return "  " + snakeElbow, true }
func (toolAdorner) tail() string              { return "" }
func (toolAdorner) liftFork() bool            { return false }
func (toolAdorner) gutterLast() bool          { return false }

// inquiryAdorner encloses the turn's deltas: the question above, the rule
// that closes the seam below, and the snake running between them. A fork is
// invariantly a property of the question, so it rises into the header.
type inquiryAdorner struct{}

func (inquiryAdorner) spineCol() int             { return 0 }
func (inquiryAdorner) textCol() int              { return 2 }
func (inquiryAdorner) anchorRow() (string, bool) { return "", true }
func (inquiryAdorner) tail() string              { return snakeClose }
func (inquiryAdorner) liftFork() bool            { return true }
func (inquiryAdorner) gutterLast() bool          { return false }

// adornerFor chooses the layout. The block's type decides, not the reader.
func adornerFor(block int, n livedoc.Node) adorner {
	switch {
	case block == ldrender.BlockInquiry:
		return inquiryAdorner{}
	case n.Type == livedoc.NodeTool:
		return toolAdorner{}
	default:
		return proseAdorner{}
	}
}

// buildAdornment is the one composition, shared by the three layouts.
func buildAdornment(a adorner, deltas map[string]livedoc.FormDelta, w int, open bool) ldrender.Adornment {
	out := ldrender.Adornment{GutterLast: a.gutterLast(), Tail: a.tail()}
	if a.liftFork() {
		if parent := forkParentOf(deltas); parent != "" {
			out.Suffix = term.StateDim(forkGlyph + " " + parent)
		}
	}
	rows := deltaRows(deltas, a.liftFork())
	if len(rows) == 0 {
		// A turn whose only state was the fork has nothing to open: the
		// header says it all, and a marker over an empty list would be a
		// gesture that answers nothing.
		return out
	}
	if !open || w < adornFloor(a) {
		// TOO NARROW TO SAY ANYTHING DRAWS THE MARKER INSTEAD. A list needs
		// the snake's column, a key and both sides of a transition; below
		// that the rows would be glyphs and ellipses, and the one column the
		// gutter costs still tells the reader there is state here.
		out.Gutter = term.StateDim(deltaGlyph)
		return out
	}
	tail := a.tail() != ""
	anchor := ldrender.SpineSlot{Kind: ldrender.SpineAnchor, Col: a.spineCol(), Tail: tail}
	if text, own := a.anchorRow(); own {
		out.Rows = append(out.Rows, adornRow(text, anchor, 0))
	} else {
		out.Head, out.HeadGlyph = anchor, spineGlyph(anchor, 0, 0)
		link := ldrender.SpineSlot{Kind: ldrender.SpineLink, Col: a.spineCol(), Tail: tail}
		out.Rows = append(out.Rows, adornRow("", link, 0))
	}
	keyw := deltaKeyWidth(rows)
	body := w - a.textCol() - ldrender.GutterCols
	for i, r := range rows {
		slot := ldrender.SpineSlot{Kind: ldrender.SpineRow, Col: a.spineCol(), Tail: tail}
		text := strings.Repeat(" ", a.textCol()) + deltaRowText(r, keyw, body)
		out.Rows = append(out.Rows, adornRow(text, slot, i+1))
	}
	return out
}

// adornFloor is the narrowest pane an open list draws in: the rows' inset,
// the key, both sides of a transition and the arrow between them.
func adornFloor(a adorner) int {
	return a.textCol() + ldrender.GutterCols + 2*formDeltaValueFloor + len(deltaArrow) + 1
}

// adornRow dresses one row of an adornment: the snake's resting glyph in
// its column, the whole row in the form-state role, and the delta index it
// answers to. Index 0 is the adornment's own chrome (the anchor, the link):
// it belongs to the block's coordinate but selects as nothing, which is why
// the selection wash stops short of it.
func adornRow(text string, slot ldrender.SpineSlot, delta int) ldrender.Row {
	text = ldrender.OverlayColumn(text, slot.Col, spineGlyph(slot, delta, 0))
	return ldrender.Row{
		Text:   term.StateDim(text),
		Delta:  delta,
		Chrome: delta == 0,
		Spine:  slot,
	}
}

// spineGlyph is the snake, one row at a time: what the slot on delta row
// `delta` carries while the cursor stands on row `cursor` (0: the cursor is
// not in this adornment at all).
//
// THE SNAKE JOINS THE CURSOR TO THE BLOCK. An adornment that hangs below
// its block reaches it upward, so the body runs from the cursor up to the
// anchor and nothing is drawn below; an enclosed one reaches its block
// through the corner beneath it, so the body runs down to the tail. With no
// cursor in the list the head parks at the anchor, and an enclosed
// adornment still draws its full body: the enclosure has to close.
func spineGlyph(slot ldrender.SpineSlot, delta, cursor int) string {
	switch slot.Kind {
	case ldrender.SpineAnchor:
		switch {
		case cursor == 0:
			return deltaGlyph
		case slot.Tail:
			return " "
		default:
			return snakeHang
		}
	case ldrender.SpineLink:
		if cursor == 0 && !slot.Tail {
			return " "
		}
		return snakeBody
	case ldrender.SpineRow:
		switch {
		case cursor == delta:
			return deltaGlyph
		case slot.Tail && (cursor == 0 || delta > cursor):
			return snakeBody
		case !slot.Tail && cursor > 0 && delta < cursor:
			return snakeBody
		}
		return " "
	}
	return ""
}

// adornRowCount is how many pseudonodes a block's deltas become: the refs
// the selection walks, and nothing about the screen.
func adornRowCount(deltas map[string]livedoc.FormDelta, lift bool) int {
	return len(deltaRows(deltas, lift))
}

// adornLift reports whether a block's coordinate lifts its fork: the
// question does, a node does not. One derivation, so the refs the selection
// walks and the rows the screen draws cannot disagree.
func adornLift(index int) bool { return index == inquiryNode }

// adornRowFull is the i'th pseudonode of a delta set, whole: what it hashes
// as and what it yanks as. i is 1-based, as nodeRef.delta is.
func adornRowFull(deltas map[string]livedoc.FormDelta, lift bool, i int) (string, bool) {
	rows := deltaRows(deltas, lift)
	if i < 1 || i > len(rows) {
		return "", false
	}
	return deltaRowFull(rows[i-1]), true
}
