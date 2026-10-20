package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

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

type metaSection struct {
	ref       nodeRef
	src       metaSource
	m         *aria.Message
	block     int
	deltas    map[string]livedoc.FormDelta
	notes     int
	open      bool
	entryOpen map[nodeRef]bool
	thread    func(text string, width int) []string
}

func buildAdornment(a adorner, sec metaSection, w int) ldrender.Adornment {
	out := ldrender.Adornment{GutterLast: a.gutterLast(), Tail: a.tail()}
	lift := a.liftFork()
	forked := false
	if lift {
		if parent := forkParentOf(sec.deltas); parent != "" {
			head := forkGlyph + " " + ariaName(parent)
			out.Suffix = term.StateDim(head)
			out.SuffixMore = otherSpot(ldrender.Width(head), otherName(parent))
			forked = true
		}
	}
	if !sec.open || w < adornFloor(a) {
		switch {
		case anyDeltaRow(sec.deltas, lift, forked):
			out.Gutter = term.StateDim(deltaGlyph)
		case sec.notes > 0:
			out.Gutter = term.CommentMark(commentGlyph)
		}
		return out
	}
	entries := sec.src.entries(*sec.m, sec.block)
	if len(entries) == 0 {
		return out
	}
	tail := a.tail() != ""
	anchor := ldrender.SpineSlot{Kind: ldrender.SpineAnchor, Col: a.spineCol(), Tail: tail}
	if text, own := a.anchorRow(); own {
		out.Rows = append(out.Rows, adornRow(text, anchor, 0, 0))
	} else {
		out.Head, out.HeadGlyph = anchor, spineGlyph(anchor, 0, 0)
		link := ldrender.SpineSlot{Kind: ldrender.SpineLink, Col: a.spineCol(), Tail: tail}
		out.Rows = append(out.Rows, adornRow("", link, 0, 0))
	}
	keyw := metaKeyWidth(entries)
	body := w - a.textCol() - ldrender.GutterCols
	indent := strings.Repeat(" ", a.textCol())
	for i, e := range entries {
		meta := i + 1
		first := i == 0 || entries[i-1].kind != e.kind
		own := ldrender.SpineSlot{Kind: ldrender.SpineRow, Col: a.spineCol(), Tail: tail, Glyph: e.glyph()}
		cont := ldrender.SpineSlot{Kind: ldrender.SpineCont, Col: a.spineCol(), Tail: tail}
		open := sec.entryOpen[metaRefOf(sec.ref, meta)]
		for k, r := range entryRows(e, keyw, body, open, sec.thread) {
			slot := own
			if k > 0 {
				slot = cont
			}
			row := styledAdornRow(e.paint(indent+r.text), slot, meta, r.reply)
			if label := entryLabel(e, first); k == 0 && label != "" {
				row.Mark = term.Dim(label)
			}
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

func adornFloor(a adorner) int {
	return a.textCol() + ldrender.GutterCols + 2*formDeltaValueFloor + len(deltaArrow) + 1
}

func adornRow(text string, slot ldrender.SpineSlot, meta, reply int) ldrender.Row {
	return styledAdornRow(term.StateDim(text), slot, meta, reply)
}

func styledAdornRow(text string, slot ldrender.SpineSlot, meta, reply int) ldrender.Row {
	text = ldrender.OverlayColumn(text, slot.Col, spineGlyph(slot, meta, 0))
	return ldrender.Row{
		Text:   text,
		Meta:   meta,
		Reply:  reply,
		Chrome: meta == 0,
		Spine:  slot,
	}
}

func spineGlyph(slot ldrender.SpineSlot, delta, cursor int) string {
	switch slot.Kind {
	case ldrender.SpineAnchor:
		switch {
		case cursor == 0:
			return headGlyph
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
			if slot.Glyph != "" {
				return slot.Glyph
			}
			return headGlyph
		case slot.Tail && (cursor == 0 || delta > cursor):
			return snakeBody
		case !slot.Tail && cursor > 0 && delta < cursor:
			return snakeBody
		}
		return " "
	case ldrender.SpineCont:
		switch {
		case slot.Tail && (cursor == 0 || delta >= cursor):
			return snakeBody
		case !slot.Tail && cursor > 0 && delta < cursor:
			return snakeBody
		}
		return " "
	}
	return ""
}

// adornLift is the one derivation shared by the selection's refs and the screen's rows.
func adornLift(index int) bool { return index == inquiryNode }
