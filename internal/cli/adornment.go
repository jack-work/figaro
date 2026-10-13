package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

// An adornment is a block's form deltas drawn as part of the block: collapsed, one
// glyph in the right gutter; open, one pseudonode row per delta joined to the
// block by a snake. The glyphs are painted plain: the row carries the rendition.
const (
	snakeHang  = "╭"
	snakeBody  = "│"
	snakeClose = "╰"
	snakeElbow = "╯"
)

// adorner is what differs between a tool, a line of prose and a turn's question.
type adorner interface {
	spineCol() int
	textCol() int
	// anchorRow is false when the snake hangs from the block's first row instead.
	anchorRow() (string, bool)
	// tail closes an enclosed adornment, drawn into the chrome beneath it.
	tail() string
	// liftFork moves a fork out of the list onto the block's chrome.
	liftFork() bool
	// gutterLast puts the collapsed marker on the block's last row.
	gutterLast() bool
}

type proseAdorner struct{}

func (proseAdorner) spineCol() int             { return 0 }
func (proseAdorner) textCol() int              { return 2 }
func (proseAdorner) anchorRow() (string, bool) { return "", false }
func (proseAdorner) tail() string              { return "" }
func (proseAdorner) liftFork() bool            { return false }
func (proseAdorner) gutterLast() bool          { return true }

// Thinking already has a gutter at column 2; the connector joins it rather than
// adding a second spine.
type thinkingAdorner struct{ proseAdorner }

func (thinkingAdorner) anchorRow() (string, bool) { return " ─" + snakeElbow, true }

type toolAdorner struct{}

func (toolAdorner) spineCol() int             { return 1 }
func (toolAdorner) textCol() int              { return 3 }
func (toolAdorner) anchorRow() (string, bool) { return "  " + snakeElbow, true }
func (toolAdorner) tail() string              { return "" }
func (toolAdorner) liftFork() bool            { return false }
func (toolAdorner) gutterLast() bool          { return false }

// inquiryAdorner encloses the deltas between the question and the closing rule; a
// fork rises into the header.
type inquiryAdorner struct{}

func (inquiryAdorner) spineCol() int             { return 0 }
func (inquiryAdorner) textCol() int              { return 2 }
func (inquiryAdorner) anchorRow() (string, bool) { return "", true }
func (inquiryAdorner) tail() string              { return snakeClose }
func (inquiryAdorner) liftFork() bool            { return true }
func (inquiryAdorner) gutterLast() bool          { return false }

func adornerFor(block int, n livedoc.Node) adorner {
	switch {
	case block == ldrender.BlockInquiry:
		return inquiryAdorner{}
	case n.Type == livedoc.NodeTool:
		return toolAdorner{}
	case n.Type == livedoc.NodeThinking:
		return thinkingAdorner{}
	default:
		return proseAdorner{}
	}
}

func buildAdornment(a adorner, deltas map[string]livedoc.FormDelta, w int, open bool) ldrender.Adornment {
	out := ldrender.Adornment{GutterLast: a.gutterLast(), Tail: a.tail()}
	lift := a.liftFork()
	forked := false
	if lift {
		if parent := forkParentOf(deltas); parent != "" {
			out.Suffix = term.StateDim(forkGlyph + " " + parent)
			forked = true
		}
	}
	// Closed is the common case on every frame: decide without building the rows.
	if !open || w < adornFloor(a) {
		if !anyDeltaRow(deltas, lift, forked) {
			return out
		}
		out.Gutter = term.StateDim(deltaGlyph)
		return out
	}
	rows := deltaRows(deltas, lift)
	if len(rows) == 0 {
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

// adornFloor is the narrowest pane an open list draws in.
func adornFloor(a adorner) int {
	return a.textCol() + ldrender.GutterCols + 2*formDeltaValueFloor + len(deltaArrow) + 1
}

// adornRow: delta 0 is the adornment's own chrome, which selects as nothing.
func adornRow(text string, slot ldrender.SpineSlot, delta int) ldrender.Row {
	text = ldrender.OverlayColumn(text, slot.Col, spineGlyph(slot, delta, 0))
	return ldrender.Row{
		Text:   term.StateDim(text),
		Delta:  delta,
		Chrome: delta == 0,
		Spine:  slot,
	}
}

// spineGlyph is the snake on delta row `delta` while the cursor is on row `cursor`
// (0: not in this adornment). A hanging adornment runs the body up to the anchor;
// an enclosed one runs it down to the tail, and draws it whole with no cursor,
// because the enclosure has to close.
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

func adornRowCount(deltas map[string]livedoc.FormDelta, lift bool) int {
	return deltaRowCount(deltas, lift, lift && forkParentOf(deltas) != "")
}

// adornLift is the one derivation shared by the selection's refs and the screen's rows.
func adornLift(index int) bool { return index == inquiryNode }

// adornRowFull: i is 1-based, as nodeRef.delta is.
func adornRowFull(deltas map[string]livedoc.FormDelta, lift bool, i int) (string, bool) {
	rows := deltaRows(deltas, lift)
	if i < 1 || i > len(rows) {
		return "", false
	}
	return deltaRowFull(rows[i-1]), true
}
