package render

import (
	"strings"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	fig "github.com/jack-work/figaro/internal/render"
)

// Block indices that are not a node's.
const (
	BlockChrome  = -2 // headers, rules, separators
	BlockInquiry = -1 // the turn's opening question
)

// Row is one composed row and the block it belongs to. Mark is the block's
// address, carried by its first row; it rides the right edge at paint time.
type Row struct {
	Text  string
	Block int
	Mark  string
	// Delta addresses one row of the block's form-delta adornment: 0 is the
	// block's own row, n the n'th delta.
	Delta int
	// Gutter is the glyph the row wears in the right gutter.
	Gutter string
	// Chrome marks a row that carries its block's coordinate but is none of
	// its content: an adornment's anchor and link rows.
	Chrome bool
	// Spine is the row's place in the adornment's snake, drawn into Text at
	// rest; a surface with a cursor resolves it again at paint time.
	Spine SpineSlot
}

// SpineKind is what a row contributes to an adornment's snake.
type SpineKind uint8

const (
	SpineNone   SpineKind = iota
	SpineAnchor           // where the snake hangs from the block
	SpineLink             // between the anchor and the first delta
	SpineRow              // a delta row: which one is Row.Delta
)

// SpineSlot is one column of one row held for the snake. Tail marks an
// adornment that closes below its last row, so the spine runs down to the
// closing corner instead of up to the block.
type SpineSlot struct {
	Kind SpineKind
	Col  int
	Tail bool
}

// Adornment is what a block's form deltas add to it.
type Adornment struct {
	// Suffix is lifted into the block's opening chrome row.
	Suffix string
	// Gutter is the collapsed marker, worn by the block's first row, or by
	// its last when GutterLast.
	Gutter     string
	GutterLast bool
	// Head is the slot the block's own first row wears; HeadGlyph is what it
	// carries at rest.
	Head      SpineSlot
	HeadGlyph string
	// Rows are the adornment's own rows, Text/Delta/Spine set; the composer
	// fills in the block.
	Rows []Row
	// Tail is what the rule below an enclosed adornment wears.
	Tail string
}

func (a Adornment) empty() bool {
	return a.Suffix == "" && a.Gutter == "" && len(a.Rows) == 0 && a.Head.Kind == SpineNone
}

// Composer turns one message into rows.
type Composer struct {
	View NodeView // required

	Header      func(role string) string
	Rule        func() string
	InputHeader func(seg aria.InquirySegment) string // an empty sender means unattributed
	// Mark is a block's address, drawn against the right edge of its first row.
	Mark func(block int, n livedoc.Node) string

	// Expanded reports whether a block draws expanded; nil means the view's default.
	Expanded func(block int) bool

	// Adorn draws a block's form-delta adornment, already styled; BlockInquiry
	// addresses the turn-level set. nil adorns nothing.
	Adorn func(block int, n livedoc.Node, deltas map[string]livedoc.FormDelta, w int) Adornment

	// Quote draws a passage the question opens by quoting, already styled.
	// nil leaves the block in the question's markdown.
	Quote func(m quote.Mention, w int, expanded bool) []string

	Tick int

	// Memo is called in place of a block's composition and must call draw()
	// whenever it has nothing to reuse. nil composes every block every time.
	Memo func(block int, n livedoc.Node, state BlockState, draw func() []Row) []Row
}

// BlockState is the fold state a block composed under; a memo keys on it.
type BlockState struct {
	Expanded bool
	Adorned  bool
}

// expandable is a view with a collapsed form.
type expandable interface {
	RenderExpanded(n livedoc.Node, width, tick int, expanded bool) []string
}

// Message composes one message: the opening question under the input header,
// the rule that closes it, then the nodes under the speaker's header.
func (c Composer) Message(m aria.Message, w int) []Row {
	if w <= 0 {
		w = 80
	}
	var adorn Adornment
	if c.Adorn != nil && len(m.FormDeltas) > 0 {
		adorn = c.Adorn(BlockInquiry, livedoc.Node{}, m.FormDeltas, w)
	}
	rows := c.Inquiry(m.Inquiry, m.InquirySegments, w, adorn)
	// A slice that carries no question carries no adornment either.
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
	// The speaker header closes the inquiry seam; a message with no question
	// is a continuation and must not announce the speaker again.
	seam := len(rows) > 0
	if seam {
		if c.Rule != nil {
			rule := clip(c.Rule(), w)
			if len(adorn.Rows) > 0 && adorn.Tail != "" {
				rule = OverlayColumn(rule, 0, adorn.Tail)
			} else {
				rows = append(rows, chrome(""))
			}
			rows = append(rows, chrome(rule))
		}
		if h := c.head(m.Role); h != "" {
			// A wrapped header desyncs the painter's row arithmetic below it.
			rows = append(rows, chrome(clip(h, w)), chrome(""))
		}
	}
	return append(rows, body...)
}

// Nodes composes a block list, one blank row between blocks.
func (c Composer) Nodes(nodes []livedoc.Node, w int) []Row {
	if w <= 0 {
		w = 80
	}
	var rows []Row
	for k, n := range nodes {
		// Minted-but-empty prose/thinking holds a node id so later ids cannot
		// shift; it draws nothing.
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
					rows[0].Spine = adorn.Head
					rows[0].Text = OverlayColumn(rows[0].Text, adorn.Head.Col, adorn.HeadGlyph)
				}
			}
		}
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

func (c Composer) render(n livedoc.Node, w, block int) []string {
	if v, ok := c.View.(expandable); ok {
		return v.RenderExpanded(n, w, c.Tick, c.Expanded == nil || c.Expanded(block))
	}
	return c.View.Render(n, w, c.Tick)
}

// Inquiry composes a turn's opening question: the input header, each
// segment's attribution, and the text. The adornment rides the header row.
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
			h = c.InputHeader(seg)
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
		rows = append(rows, c.quoted(seg.Text, w)...)
		if k == 0 && c.Mark != nil && len(rows) > first {
			rows[first].Mark = c.Mark(BlockInquiry, livedoc.Node{})
		}
	}
	// The heading and the first line are the block's head, which the pager's
	// sticky header pins; breathing room goes below, in the seam.
	return rows
}

// quoted composes a segment's text: a quoted passage as chrome, then prose.
func (c Composer) quoted(text string, w int) []Row {
	if c.Quote == nil {
		return prose(text, w, BlockInquiry)
	}
	m, rest, ok := quote.Mentioned(text)
	if !ok {
		return prose(text, w, BlockInquiry)
	}
	var rows []Row
	for _, line := range c.Quote(m, w, c.Expanded != nil && c.Expanded(BlockInquiry)) {
		rows = append(rows, Row{Text: clip(line, w), Block: BlockInquiry})
	}
	if strings.TrimSpace(rest) == "" {
		return rows
	}
	if len(rows) > 0 {
		rows = append(rows, Row{Block: BlockInquiry})
	}
	return append(rows, prose(rest, w, BlockInquiry)...)
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

// Text drops the block addressing.
func Text(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out
}
