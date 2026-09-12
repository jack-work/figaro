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
	// State marks a row of the block's FORM DELTA TABLE rather than of the
	// block itself. The table is drawn under its block and shares its
	// coordinate, but a surface with a selection addresses it separately.
	State bool
}

// Composer turns one message into rows.
type Composer struct {
	View NodeView // draws one block; required

	Header func(role string) string // voice header, e.g. "< figaro"
	Rule   func() string            // the separator between the two voices
	Sender func(string) string      // styles a segment's attribution
	// Mark is a block's address, drawn against the right edge of its first
	// row. nil draws none.
	Mark func(block int, n livedoc.Node) string

	// Expanded reports whether a block draws in its expanded form. Only the
	// pager has an expansion gesture; nil means "the view's default".
	Expanded func(block int) bool

	// State draws a node's (or the turn's) form deltas beneath it, already
	// styled and wrapped by the surface. The block is the same coordinate
	// Expanded answers for, so the pager's gesture can open a collapsed
	// delta; BlockInquiry addresses the turn-level set. nil draws nothing,
	// which is every surface that has not opted in.
	State func(block int, deltas map[string]livedoc.FormDelta, w int) []string

	Tick int // animation frame for spinners
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
	rows := c.Inquiry(m.Inquiry, m.InquirySegments, w)
	// The turn's own form deltas sit under the question they arrived with,
	// in the inquiry's Block coordinate, after one blank row.
	if c.State != nil && len(m.FormDeltas) > 0 {
		if state := c.State(BlockInquiry, m.FormDeltas, w); len(state) > 0 {
			rows = append(rows, Row{Text: "", Block: BlockInquiry, State: true})
			for _, l := range state {
				rows = append(rows, Row{Text: clip(l, w), Block: BlockInquiry, State: true})
			}
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
		rows = append(rows, chrome(""))
		if c.Rule != nil {
			rows = append(rows, chrome(clip(c.Rule(), w)))
		}
		if h := c.head(m.Role); h != "" {
			rows = append(rows, chrome(h), chrome(""))
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
		first := len(rows)
		for _, l := range c.render(n, w, k) {
			rows = append(rows, Row{Text: clip(l, w), Block: k})
		}
		if c.Mark != nil && len(rows) > first {
			rows[first].Mark = c.Mark(k, n)
		}
		// The node's form deltas, below the block they explain and sharing
		// its Block coordinate, marked State so a surface with a selection
		// can address the table on its own. One blank row separates them
		// from the block's body.
		if c.State != nil && len(n.FormDeltas) > 0 {
			if state := c.State(k, n.FormDeltas, w); len(state) > 0 {
				rows = append(rows, Row{Text: "", Block: k, State: true})
				for _, l := range state {
					rows = append(rows, Row{Text: clip(l, w), Block: k, State: true})
				}
			}
		}
	}
	return rows
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
// attribution of each segment, and the text.
func (c Composer) Inquiry(inquiry string, segments []aria.InquirySegment, w int) []Row {
	if strings.TrimSpace(inquiry) == "" {
		return nil
	}
	var rows []Row
	if h := c.head(livedoc.RoleInput); h != "" {
		rows = append(rows, chrome(h), chrome(""))
	}
	first := len(rows)
	if len(segments) == 0 {
		rows = append(rows, prose(inquiry, w, BlockInquiry)...)
	} else {
		for k, seg := range segments {
			if k > 0 {
				rows = append(rows, Row{Text: "", Block: BlockInquiry})
			}
			if seg.Sender != "" && c.Sender != nil {
				// Indented to sit under the prose, which render.Prose insets.
				rows = append(rows, Row{Text: clip(c.Sender("  "+seg.Sender), w), Block: BlockInquiry})
			}
			rows = append(rows, prose(seg.Text, w, BlockInquiry)...)
		}
	}
	if c.Mark != nil && len(rows) > first {
		rows[first].Mark = c.Mark(BlockInquiry, livedoc.Node{})
	}
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
