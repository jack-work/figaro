package render

import (
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	fig "github.com/jack-work/figaro/internal/render"
)

// A message has ONE shape, and this is where it is decided.
//
// It used to be decided three times — Incipit.messageRows for the live view,
// transcript.renderMsgBase for the pager, cli.renderTurnRows for `show` — each
// with its own inquiry renderer, its own node walk and its own idea of where
// the blanks go. They drifted, as three copies do: the pager rendered nodes two
// columns narrower than the incipit and indented them one further, and `show`
// printed verbose detail nobody had asked for. Conformance is not something you
// can test three implementations into; it is something you get by having one.
//
// Composer is that one. Every surface supplies what is genuinely its own — the
// view that draws a block, the chrome hooks, whether a block is expanded — and
// receives the same rows in the same order.

// Block indices that are not a node's. A row carries the index of the block it
// belongs to, which is what lets the pager address, select and highlight a row
// without re-deriving the structure it was composed from.
const (
	BlockChrome  = -2 // headers, rules, separators: belongs to no block
	BlockInquiry = -1 // the turn's opening question (aria.Message.Inquiry)
)

// Row is one composed row and the block it belongs to.
type Row struct {
	Text  string
	Block int
}

// Composer turns one message into rows.
//
// The zero value composes nothing but the nodes: every chrome hook is optional,
// and a nil hook draws nothing rather than a placeholder.
type Composer struct {
	View NodeView // draws one block; required

	Header func(role string) string // voice header, e.g. "< figaro"
	Rule   func() string            // the separator between the two voices
	Sender func(string) string      // styles a segment's attribution
	Coord  func(block int, n livedoc.Node) string

	// Expanded reports whether a block draws in its expanded form. Only the
	// pager has an expansion gesture; nil means "the view's default".
	Expanded func(block int) bool

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
//
// The question is TEXT ON THE TURN, not a node, so no node walk can produce it
// — which is why it is drawn here and nowhere else.
//
// No header over an empty run: a message whose blocks all render to nothing
// (minted-but-empty prose, a tool already drawn) must not print a header over
// empty space.
func (c Composer) Message(m aria.Message, w int) []Row {
	if w <= 0 {
		w = 80
	}
	rows := c.inquiry(m.Inquiry, m.InquirySegments, w)
	body := c.Nodes(m.Nodes, w)
	if len(body) == 0 {
		return rows
	}
	// The speaker header closes the inquiry seam; it does not open a node run.
	// A message with no question is a CONTINUATION of one already on screen —
	// a page window that opened mid-turn, or a unit cut off an oversize turn —
	// and announcing the speaker again asserts a boundary the turn does not
	// have.
	//
	// Named, because the test is not "are there rows" but "was a question
	// drawn": rows can only have come from c.inquiry today, and anything
	// prepended above would silently bring the ghost back.
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

// Nodes composes a block list: each block, one blank row between them, and —
// when the surface asks for it — a coordinate label above each.
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
		if c.Coord != nil {
			if l := c.Coord(k, n); l != "" {
				rows = append(rows, Row{Text: clip(l, w), Block: k})
			}
		}
		for _, l := range c.render(n, w, k) {
			rows = append(rows, Row{Text: clip(l, w), Block: k})
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
//
// ONE header for the whole question however many people wrote it — the
// submissions folded into one message, and a header apiece would say otherwise.
// Senders are drawn per segment instead, dim, above their text, and an UNKNOWN
// sender draws NOTHING: most messages ever written carry none, and a
// placeholder on each would be noise where there used to be none.
func (c Composer) inquiry(inquiry string, segments []aria.InquirySegment, w int) []Row {
	if strings.TrimSpace(inquiry) == "" {
		return nil
	}
	var rows []Row
	if h := c.head(livedoc.RoleInput); h != "" {
		rows = append(rows, chrome(h), chrome(""))
	}
	if c.Coord != nil {
		// The question's arrival time lives on aria.Turn.At, which the pager's
		// unit does not carry. The ADDRESS is what a jump needs.
		if l := c.Coord(BlockInquiry, livedoc.Node{}); l != "" {
			rows = append(rows, Row{Text: clip(l, w), Block: BlockInquiry})
		}
	}
	// Deliberately NOT table-clamped: a node is the agent's output and may be
	// summarised, but the question is the user's own text and figaro does not
	// truncate what the user wrote.
	if len(segments) == 0 {
		return append(rows, prose(inquiry, w, BlockInquiry)...)
	}
	for k, seg := range segments {
		if k > 0 {
			rows = append(rows, Row{Text: "", Block: BlockInquiry})
		}
		if seg.Sender != "" && c.Sender != nil {
			// Indented to sit under the prose, which render.Prose insets. A
			// flush-left attribution over an inset paragraph reads as a heading
			// rather than as a label on the text below it.
			rows = append(rows, Row{Text: clip(c.Sender("  "+seg.Sender), w), Block: BlockInquiry})
		}
		rows = append(rows, prose(seg.Text, w, BlockInquiry)...)
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
