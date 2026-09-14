package render

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	fig "github.com/jack-work/figaro/internal/render"
)

// A message has ONE shape, and this is where it is decided.

// Block indices that are not a node's. A row carries the index of the block it
// belongs to, which is what lets the pager address, select and highlight a row
// without re-deriving the structure it was composed from.
const (
	BlockChrome  = -2 // headers, rules, separators: belongs to no block
	BlockInquiry = -1 // the turn's opening question (aria.Message.Inquiry)
)

// Row is one composed row and the block it belongs to. Mark is the block's
// address, carried by its first row for a surface that draws addresses: it
// rides the right edge at paint time, so turning it on moves nothing.
type Row struct {
	Text  string
	Block int
	Mark  string
	// Delta addresses one row of the block's form-delta ADORNMENT rather
	// than the block itself: 0 is the block's own row, n the n'th delta.
	// Each delta is a pseudonode of its own, so a surface with a selection
	// walks them one at a time.
	Delta int
	// Gutter is the glyph the row wears in the right gutter: the marker a
	// block with a collapsed adornment shows.
	Gutter string
	// Chrome marks a row that carries its block's coordinate but is none of
	// its content: an adornment's anchor and link rows. A surface with a
	// selection draws no cue on it, which is why the wash stops at the
	// blank row above a delta list instead of swallowing it.
	Chrome bool
	// Spine is the row's place in the adornment's snake. The glyph is drawn
	// into Text in its resting form; a surface with a cursor resolves it
	// again at paint time, because the marker follows the selection.
	Spine SpineSlot
}

// SpineKind is what a row contributes to an adornment's snake.
type SpineKind uint8

const (
	SpineNone   SpineKind = iota // the row is not part of a snake
	SpineAnchor                  // where the snake hangs from the block
	SpineLink                    // between the anchor and the first delta
	SpineRow                     // a delta row: which one is Row.Delta
)

// SpineSlot is one column of one row held for the snake: which column, and
// what the row contributes. Tail marks an adornment that closes BELOW its
// last row (the inquiry's, enclosed between the question and the rule), so
// the spine runs from the cursor down to the closing corner instead of up
// to the block.
type SpineSlot struct {
	Kind SpineKind
	Col  int
	Tail bool
}

// Adornment is what a block's form deltas add to it. One shape, three
// layouts (see the adorners in internal/cli): the glyph a collapsed block
// wears in the right gutter, the suffix a fork lifts into its chrome, the
// rows the open list draws, and the corner that closes an enclosed one.
type Adornment struct {
	// Suffix is lifted into the block's opening chrome row: the fork glyph
	// and the aria a turn was forked from.
	Suffix string
	// Gutter is the collapsed marker, worn by the block's first row, or by
	// its last when GutterLast (prose tacks it onto the end of its text).
	Gutter     string
	GutterLast bool
	// Head is the slot the BLOCK's own first row wears: prose hangs its
	// snake from the line it adorns rather than from a row of its own.
	// HeadGlyph is what that slot carries at rest.
	Head      SpineSlot
	HeadGlyph string
	// Rows are the adornment's own rows, Text/Delta/Spine set; the composer
	// fills in the block.
	Rows []Row
	// Tail is what the rule below the adornment wears, for a type whose
	// adornment is enclosed by the chrome under it.
	Tail string
}

// empty reports an adornment that draws nothing at all.
func (a Adornment) empty() bool {
	return a.Suffix == "" && a.Gutter == "" && len(a.Rows) == 0 && a.Head.Kind == SpineNone
}

// Composer turns one message into rows.
type Composer struct {
	View NodeView // draws one block; required

	Header      func(role string) string   // voice header, e.g. "< figaro"
	Rule        func() string              // the separator between the two voices
	InputHeader func(sender string) string // inquiry heading; empty sender means unattributed
	// Mark is a block's address, drawn against the right edge of its first
	// row. nil draws none.
	Mark func(block int, n livedoc.Node) string

	// Expanded reports whether a block draws in its expanded form. Only the
	// pager has an expansion gesture; nil means "the view's default".
	Expanded func(block int) bool

	// Adorn draws a block's form-delta adornment, already styled by the
	// surface. The block is the same coordinate Expanded answers for, and
	// BlockInquiry addresses the turn-level set. nil adorns nothing, which
	// is every surface that has not opted in.
	Adorn func(block int, n livedoc.Node, deltas map[string]livedoc.FormDelta, w int) Adornment

	Tick int // animation frame for spinners

	// Memo lets a surface reuse the rows a block composed last time instead of
	// composing it again. It is called in place of the composition and must
	// call draw() whenever it has nothing to reuse. nil composes every block
	// every time.
	Memo func(block int, n livedoc.Node, state BlockState, draw func() []Row) []Row
}

// BlockState is the fold state a block composed under: its own body, and its
// adornment. Both are the surface's, and both change what draw() produces,
// so a memo keys on the pair.
type BlockState struct {
	Expanded bool
	Adorned  bool
}

// expandable is the view side of the pager's expansion gesture. A view that
// does not implement it simply has no collapsed form.
type expandable interface {
	RenderExpanded(n livedoc.Node, width, tick int, expanded bool) []string
}

// Message composes one message: the turn's opening question under the input
// header, the rule that closes it, then the nodes under the speaker's header,
// one blank row between blocks.
func (c Composer) Message(m aria.Message, w int) []Row {
	if w <= 0 {
		w = 80
	}
	var adorn Adornment
	if c.Adorn != nil && len(m.FormDeltas) > 0 {
		adorn = c.Adorn(BlockInquiry, livedoc.Node{}, m.FormDeltas, w)
	}
	rows := c.Inquiry(m.Inquiry, m.InquirySegments, w, adorn)
	// The turn's own form deltas hang under the question they arrived with,
	// in the inquiry's Block coordinate. A slice that carries no question
	// carries no adornment either: there is nothing for it to hang from.
	if len(rows) > 0 {
		for _, r := range adorn.Rows {
			r.Block, r.Text = BlockInquiry, clip(r.Text, w)
			rows = append(rows, r)
		}
	}
	body := c.Nodes(m.Nodes, w)
	if len(body) == 0 {
		return rows
	}
	// The speaker header closes the inquiry seam; it does not open a node run.
	// A message with no question is a CONTINUATION of one already on screen,
	// a page window that opened mid-turn, or a unit cut off an oversize turn,
	// and announcing the speaker again asserts a boundary the turn does not
	// have.
	seam := len(rows) > 0
	if seam {
		if c.Rule != nil {
			rule := clip(c.Rule(), w)
			if len(adorn.Rows) > 0 && adorn.Tail != "" {
				// The rule is the adornment's closing corner: the question's
				// deltas are ENCLOSED by the chrome beneath them.
				rule = OverlayColumn(rule, 0, adorn.Tail)
			} else {
				// A blank above the rule, exactly as between two messages
				// (the pager's sepRows): the question ends, then the seam.
				// An open delta list needs no blank -- it opens with an
				// anchor row of its own and closes into the corner.
				rows = append(rows, chrome(""))
			}
			rows = append(rows, chrome(rule))
		}
		if h := c.head(m.Role); h != "" {
			// CLIPPED LIKE EVERY OTHER ROW. A header wider than the pane
			// wraps, and a wrapped row desyncs the painter's one row per
			// line arithmetic for everything below it.
			rows = append(rows, chrome(clip(h, w)), chrome(""))
		}
	}
	return append(rows, body...)
}

// Nodes composes a block list: each block, one blank row between them, and -
// when the surface asks for it, a coordinate label above each.
func (c Composer) Nodes(nodes []livedoc.Node, w int) []Row {
	if w <= 0 {
		w = 80
	}
	var rows []Row
	for k, n := range nodes {
		// Minted-but-empty prose/thinking (skills/figaro/reference/turns.md, invariant 6)
		// holds a node id so later ids cannot shift; it draws nothing.
		if n.Type != livedoc.NodeTool && strings.TrimSpace(n.Markdown) == "" {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, chrome(""))
		}
		rows = append(rows, c.block(n, w, k)...)
	}
	return rows
}

// block composes one node's rows: its body, its address, and the form deltas
// that adorn it. A surface with a Memo may answer from what it kept.
func (c Composer) block(n livedoc.Node, w, k int) []Row {
	state := BlockState{}
	if _, ok := c.View.(expandable); ok {
		state.Expanded = c.Expanded == nil || c.Expanded(k)
	}
	var adorn Adornment
	if c.Adorn != nil && len(n.FormDeltas) > 0 {
		adorn = c.Adorn(k, n, n.FormDeltas, w)
		state.Adorned = len(adorn.Rows) > 0
	}
	draw := func() []Row {
		var rows []Row
		for _, l := range c.render(n, w, k) {
			rows = append(rows, Row{Text: clip(l, w), Block: k})
		}
		if len(rows) > 0 {
			if c.Mark != nil {
				rows[0].Mark = c.Mark(k, n)
			}
			if !adorn.empty() {
				at := 0
				if adorn.GutterLast {
					at = len(rows) - 1
				}
				rows[at].Gutter = adorn.Gutter
				rows[at].Text = OverlayGutter(rows[at].Text, adorn.Gutter, w)
				if adorn.Head.Kind != SpineNone {
					// Prose hangs its snake from the line it adorns: the
					// glyph stands in the margin render.Prose leaves.
					rows[0].Spine = adorn.Head
					rows[0].Text = OverlayColumn(rows[0].Text, adorn.Head.Col, adorn.HeadGlyph)
				}
			}
		}
		// The adornment's own rows, below the block they explain and sharing
		// its Block coordinate, each addressed by its delta index so a
		// surface with a selection can walk them one at a time.
		for _, r := range adorn.Rows {
			r.Block, r.Text = k, clip(r.Text, w)
			rows = append(rows, r)
		}
		return rows
	}
	if c.Memo != nil {
		return c.Memo(k, n, state, draw)
	}
	return draw()
}

// render draws one block, in its expanded form when the surface says so.
func (c Composer) render(n livedoc.Node, w, block int) []string {
	if v, ok := c.View.(expandable); ok {
		return v.RenderExpanded(n, w, c.Tick, c.Expanded == nil || c.Expanded(block))
	}
	return c.View.Render(n, w, c.Tick)
}

// inquiry draws the question that opened the turn, attributed when it can be.
// Inquiry composes a turn's opening question: the input header, the
// attribution of each segment, and the text. The adornment's fork suffix and
// collapsed marker ride the header row, which is where a fork belongs: it is
// a property of the turn, not of a line of its text.
func (c Composer) Inquiry(inquiry string, segments []aria.InquirySegment, w int, adorn Adornment) []Row {
	if strings.TrimSpace(inquiry) == "" {
		return nil
	}
	if len(segments) == 0 {
		segments = []aria.InquirySegment{{Text: inquiry}}
	}
	var rows []Row
	for k, seg := range segments {
		if k > 0 {
			rows = append(rows, chrome(""))
		}
		h := c.head(livedoc.RoleInput)
		if c.InputHeader != nil {
			h = c.InputHeader(seg.Sender)
		}
		if h != "" {
			if k == 0 && adorn.Suffix != "" {
				h += " " + adorn.Suffix
			}
			head := chrome(clip(h, w))
			if k == 0 {
				head.Text = OverlayGutter(head.Text, adorn.Gutter, w)
				head.Gutter = adorn.Gutter
			}
			rows = append(rows, head)
		}
		first := len(rows)
		rows = append(rows, prose(seg.Text, w, BlockInquiry)...)
		if k == 0 && c.Mark != nil && len(rows) > first {
			rows[first].Mark = c.Mark(BlockInquiry, livedoc.Node{})
		}
	}
	// The heading sits ON the first line it introduces: those two rows are
	// the block's head, and the pager's sticky header pins exactly them (see
	// transcript_sticky.go). The question's breathing room is BELOW, in the
	// seam Message draws under it.
	return rows
}

func (c Composer) head(role string) string {
	if c.Header == nil {
		return ""
	}
	return c.Header(role)
}

func chrome(s string) Row { return Row{Text: s, Block: BlockChrome} }

func prose(text string, w, block int) []Row {
	var rows []Row
	for _, l := range fig.Prose(text, w) {
		rows = append(rows, Row{Text: clip(l, w), Block: block})
	}
	return rows
}

// Text drops the block addressing, for the surfaces that only print.
func Text(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out
}
