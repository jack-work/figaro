package cli

// FROM A ROW ON SCREEN TO A RUNE IN THE LOG.
//
// A node's rows are glamour's: reflowed, wrapped, indented, bulleted, with
// `**bold**` turned into bold and the asterisks gone. render.Row carries no
// provenance, and glamour cannot be asked for any. So the mapping is
// recovered after the fact by walking the node's source and its rendered
// rows together, rune by rune:
//
//	a row rune that matches the next source rune (within a short lookahead)
//	CONSUMES it and records the source index it stood for;
//	a row rune that matches nothing nearby was INSERTED by the renderer
//	(a bullet, a gutter, an indent) and records nothing;
//	source runes the walk skipped over were syntax the renderer ATE.
//
// The result is, per row, the source rune each visible column stands for.
// Where alignment is lost the row is marked and the coordinate WIDENS to the
// nearest aligned boundary; it never guesses a narrower range than it can
// justify, and the status row says when it widened.
//
// This is plan §6 option A, built directly rather than after option C: the
// per-rune walk yields the per-line map as a by-product.

import (
	"strings"
	"unicode"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/render"
	"github.com/mattn/go-runewidth"
)

// sourceRow is one rendered row's provenance.
type sourceRow struct {
	// cols[c] is the source rune index the visible column c stands for, or
	// -1 for an inserted cell. Wide runes occupy two columns and repeat.
	cols []int
	// start and end bound the source runes this row covers, half open.
	// aligned is false when too few of the row's runes found a home, in which
	// case start and end are not to be trusted.
	start, end int
	aligned    bool
}

// alignLookahead bounds how far past the cursor a row rune may look for its
// source rune. Markdown syntax between two visible runes is short: "**",
// "](", a backtick, a heading's "## ". A row rune that is further than this
// from anything matching is an insertion, not a skip.
const alignLookahead = 48

// alignFloor is the share of a row's visible non-space runes that must find
// a source rune for the row to count as aligned.
const alignFloor = 0.6

// alignRun is the shortest run of CONSECUTIVE matches a row must contain to
// anchor. Single-rune hits are how a chrome row ("· prose", a tool's header)
// finds its letters scattered through a paragraph; text that was rendered
// from the source runs alongside it.
const alignRun = 4

// alignRows walks source and rows together. rows are the rendered rows with
// escapes still in; they are stripped here. A row that does not align does
// not move the cursor, so chrome between two text rows cannot push the
// second one off its place.
func alignRows(source string, rows []string) []sourceRow {
	src := []rune(source)
	out := make([]sourceRow, len(rows))
	cursor := 0
	for i, raw := range rows {
		sr, next := alignRow(src, cursor, render.StripEscapes(raw))
		if sr.aligned {
			cursor = next
		}
		out[i] = sr
	}
	return out
}

// alignRow aligns one row from cursor and reports where the cursor would
// stand after it.
func alignRow(src []rune, cursor int, plain string) (sourceRow, int) {
	sr := sourceRow{start: -1, end: -1}
	matched, tried, run, longest := 0, 0, 0, 0
	for _, r := range []rune(plain) {
		w := runeCells(r)
		hit := -1
		if !unicode.IsSpace(r) {
			tried++
			// Source whitespace between two visible runes is not a break in
			// the run: the renderer folds it, it does not delete it.
			next := cursor
			for next < len(src) && unicode.IsSpace(src[next]) {
				next++
			}
			for k := cursor; k < len(src) && k < cursor+alignLookahead; k++ {
				if src[k] == r {
					hit = k
					break
				}
			}
			if hit == next {
				run++
			} else if hit >= 0 {
				run = 1
			} else {
				run = 0
			}
			if run > longest {
				longest = run
			}
		}
		if hit >= 0 {
			matched++
			cursor = hit + 1
			if sr.start < 0 {
				sr.start = hit
			}
			sr.end = hit + 1
		}
		for range w {
			sr.cols = append(sr.cols, hit)
		}
	}
	need := alignRun
	if tried < need {
		need = tried
	}
	sr.aligned = tried > 0 && float64(matched) >= alignFloor*float64(tried) && longest >= need
	if !sr.aligned {
		sr.start, sr.end = -1, -1
	}
	return sr, cursor
}

func runeCells(r rune) int {
	if w := runewidth.RuneWidth(r); w > 0 {
		return w
	}
	return 1
}

// ---------------------------------------------------------------------------
// The node's side: which text, at which coordinate.

// nodeQuoteSource is the text a node's rows were rendered from and the log
// coordinate that text lives at. base is the rune offset of `text` within
// the block (a clamped tool tail starts partway in). ok is false for a node
// that cannot be quoted, with why saying so in one sentence.
func (t *transcript) nodeQuoteSource(ref nodeRef) (text string, at quote.Coord, base int, why string) {
	if ref.delta {
		return "", quote.Coord{}, 0, "form state is not quotable: it is state, not something anyone said"
	}
	if ref.index == inquiryNode {
		m, ok := t.messageOf(ref.turn)
		if !ok || m.Inquiry == "" {
			return "", quote.Coord{}, 0, "the question is not on screen"
		}
		if m.InquiryLT == 0 {
			return "", quote.Coord{}, 0, "the question has no coordinate until its turn is sealed"
		}
		if len(m.InquirySegments) > 1 {
			return "", quote.Coord{}, 0, "a question asked by several senders cannot be quoted yet"
		}
		return m.Inquiry, quote.Coord{LT: m.InquiryLT, Block: 0}, 0, ""
	}
	n, ok := t.nodeNear(ref)
	if !ok {
		return "", quote.Coord{}, 0, "the selection is no longer on screen"
	}
	switch n.Type {
	case livedoc.NodeTool:
		if len(n.Src) < 2 {
			return "", quote.Coord{}, 0, "a tool with no result yet has no text to quote"
		}
		if styleFor(n.Name).Body != "" {
			return "", quote.Coord{}, 0, "this tool shows an argument, not its result, and arguments are not quotable yet"
		}
		// The rows are the output's TAIL when the node is folded: the walk runs
		// over that tail, and base carries the offset back to the block.
		src := n.Src[len(n.Src)-1]
		raw := strings.TrimRight(n.Output, "\n")
		shown, _ := tailOutput(raw, t.toolCap(ref))
		base = len([]rune(raw)) - len([]rune(shown))
		return shown, quote.Coord{LT: src.LT, Block: src.Block}, base, ""
	default:
		if len(n.Src) == 0 {
			return "", quote.Coord{}, 0, "this block carries no log coordinate"
		}
		return n.Markdown, quote.Coord{LT: n.Src[0].LT, Block: n.Src[0].Block}, 0, ""
	}
}

// messageOf finds the slice that starts turn. It walks the window from that
// turn's anchor, not from its start: a coordinate over a wide range must not
// cost a copy of every message between its ends.
func (t *transcript) messageOf(turn int) (aria.Message, bool) {
	var found aria.Message
	ok := false
	at := aria.Anchor{Turn: uint64(turn)}
	if at.Less(t.from) {
		at = t.from
	}
	t.client.ForEachIn(at, windowEnd, func(m aria.Message) bool {
		if m.Turn > turn {
			return false
		}
		if m.Turn == turn && m.From == 0 {
			found, ok = m, true
			return false
		}
		return true
	})
	if ok {
		return found, true
	}
	if open := t.openMessage(); open != nil && open.Turn == turn && open.From == 0 {
		return *open, true
	}
	return aria.Message{}, false
}

// nodeNear is nodeAt without the walk from the window's start: it begins at
// the node's own anchor.
func (t *transcript) nodeNear(ref nodeRef) (livedoc.Node, bool) {
	var node livedoc.Node
	ok := false
	at := aria.Anchor{Turn: uint64(ref.turn), Node: uint64(max(ref.index, 0))}
	if at.Less(t.from) {
		at = t.from
	}
	t.client.ForEachIn(at, windowEnd, func(m aria.Message) bool {
		if m.Turn != ref.turn {
			return m.Turn < ref.turn
		}
		for i := range m.Nodes {
			if nodeRefAt(m, i) == ref {
				node, ok = m.Nodes[i], true
				return false
			}
		}
		return true
	})
	if ok {
		return node, true
	}
	if open := t.openMessage(); open != nil && open.Turn == ref.turn {
		for i := range open.Nodes {
			if nodeRefAt(*open, i) == ref {
				return open.Nodes[i], true
			}
		}
	}
	return livedoc.Node{}, false
}

// toolCap is the output tail the pager draws for a tool, which is what a
// selection over a tool's rows can address.
func (t *transcript) toolCap(ref nodeRef) int {
	if t.expanded[ref] {
		return nodeOutputUnlimited
	}
	return nodeBashCapDefault
}

// nodeRows are the cached rows that belong to ref, in order, as painted.
func (t *transcript) nodeRows(ref nodeRef) []string {
	var out []string
	for k := t.entriesOfTurn(ref.turn); k >= 0 && k < len(t.index.entries); k++ {
		e := &t.index.entries[k]
		if e.turn != ref.turn {
			break
		}
		if e.isGap() {
			continue
		}
		for i := range e.rows {
			if e.rows[i].ref == ref {
				out = append(out, e.rows[i].text)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The selection's coordinate.

// visualRange resolves the visual selection to a quote range. note is set
// when the range was widened past an unaligned row; err is one sentence when
// it cannot be resolved at all.
func (t *transcript) visualRange() (r quote.Range, note string, err string) {
	if !t.visual.highlighted() {
		return quote.Range{}, "", "no highlight (v or V at the cursor to start one)"
	}
	// AN END THAT HAS PHASED OUT OF THE RETAINED WINDOW IS A REFUSAL. The
	// pager evicts messages far behind what it shows; the wash keeps painting
	// to the window's edge so the reader sees the range is still open, but a
	// coordinate for text this process no longer holds would be a guess.
	// Scrolling up pages it back in, and the sentence says so.
	if _, ok := t.visualLineOf(t.visual.anchor); !ok {
		return quote.Range{}, "", "the start of the selection is no longer in memory (scroll up to page it back in)"
	}
	if _, ok := t.visualLineOf(t.visual.cursor); !ok {
		return quote.Range{}, "", "the cursor's end of the selection is no longer in memory"
	}
	s := t.visualSpan()
	if !s.active() {
		return quote.Range{}, "", "the selection is no longer on screen"
	}
	lo, lok := t.visualPointAt(s.loLine, s.loCol)
	hi, hok := t.visualPointAt(s.hiLine, s.hiCol)
	if !lok || !hok {
		return quote.Range{}, "", "the selection is no longer on screen"
	}
	startC, startOff, wl, werr := t.resolveEnd(lo, s, true)
	if werr != "" {
		return quote.Range{}, "", werr
	}
	endC, endOff, wr, werr := t.resolveEnd(hi, s, false)
	if werr != "" {
		return quote.Range{}, "", werr
	}
	r = quote.Range{
		Start:   quote.Coord{LT: startC.LT, Block: startC.Block, Offset: startOff},
		End:     quote.Coord{LT: endC.LT, Block: endC.Block, Offset: endOff},
		Block:   true,
		Offsets: true,
	}
	if !r.Span() && r.End.Offset <= r.Start.Offset {
		return quote.Range{}, "", "the selection covers no text"
	}
	switch {
	case wl && wr:
		note = "widened both ends to the nearest text the screen could be matched to"
	case wl:
		note = "widened the start to the nearest text the screen could be matched to"
	case wr:
		note = "widened the end to the nearest text the screen could be matched to"
	}
	return r, note, ""
}

// resolveEnd turns one endpoint into a coordinate and a rune offset. For the
// start it is the first rune the endpoint's cell stands for; for the end,
// one past the last. widened reports that the row was not aligned and the
// offset came from a neighbour.
func (t *transcript) resolveEnd(p visualPoint, s visualSpan, isStart bool) (quote.Coord, int, bool, string) {
	text, at, base, why := t.nodeQuoteSource(p.ref)
	if why != "" {
		return quote.Coord{}, 0, false, why
	}
	rows := t.nodeRows(p.ref)
	if p.row >= len(rows) {
		return quote.Coord{}, 0, false, "the selection is no longer on screen"
	}
	aligned := alignRows(text, rows)
	total := len([]rune(text))
	row := aligned[p.row]
	if row.aligned {
		if s.kind == visualLine {
			if isStart {
				return at, base + row.start, false, ""
			}
			return at, base + row.end, false, ""
		}
		// Char mode: the column's own rune, snapping an inserted cell to the
		// next (start) or previous (end) real one on the row.
		if off, inclusive, ok := colToRune(row, p.col, isStart); ok {
			if isStart || !inclusive {
				return at, base + off, false, ""
			}
			return at, base + off + 1, false, ""
		}
	}
	// Unaligned: widen outward to the nearest aligned row of this node.
	if isStart {
		for i := p.row - 1; i >= 0; i-- {
			if aligned[i].aligned {
				return at, base + aligned[i].end, true, ""
			}
		}
		return at, base, true, ""
	}
	for i := p.row + 1; i < len(aligned); i++ {
		if aligned[i].aligned {
			return at, base + aligned[i].start, true, ""
		}
	}
	return at, base + total, true, ""
}

// colToRune is the source rune a column stands for, snapping past inserted
// cells toward the range's interior. For an end, inclusive says the rune is
// part of the range (the cell stood on it or on padding after it); an end
// that sits in the indent BEFORE a row's first rune names that rune as the
// exclusive bound instead, so the row contributes nothing rather than
// everything.
func colToRune(row sourceRow, col int, isStart bool) (off int, inclusive, ok bool) {
	if len(row.cols) == 0 {
		return 0, false, false
	}
	if col >= len(row.cols) {
		col = len(row.cols) - 1
	}
	if col < 0 {
		col = 0
	}
	if isStart {
		for c := col; c < len(row.cols); c++ {
			if row.cols[c] >= 0 {
				return row.cols[c], true, true
			}
		}
		return 0, false, false
	}
	for c := col; c >= 0; c-- {
		if row.cols[c] >= 0 {
			return row.cols[c], true, true
		}
	}
	for c := col; c < len(row.cols); c++ {
		if row.cols[c] >= 0 {
			return row.cols[c], false, true
		}
	}
	return 0, false, false
}

// visualCoordinate is the qualified token for the selection, or an error
// sentence: what `:<,>` expands to and what Y yanks.
func (t *transcript) visualCoordinate() (token, note, err string) {
	r, note, err := t.visualRange()
	if err != "" {
		return "", "", err
	}
	return quote.Format(r), note, ""
}

// quotePreview is a short spelling of what a range covers, for the status
// row: the first few words of the source at the start offset.
func quotePreview(text string, r quote.Range) string {
	runes := []rune(text)
	if r.Start.Offset >= len(runes) {
		return ""
	}
	end := r.End.Offset
	if r.Span() || end > len(runes) {
		end = len(runes)
	}
	s := strings.TrimSpace(string(runes[r.Start.Offset:end]))
	if len([]rune(s)) > 40 {
		s = string([]rune(s)[:40]) + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Surviving a reflow. A visualPoint is (node, row, column) at ONE width; a
// resize re-wraps every node and the same row index now names different
// text. The point is pinned to a source rune before the reflow and found
// again after it, so the wash follows the text rather than the row number.

type visualPin struct {
	p   visualPoint
	off int  // source rune offset the cell stood for
	ok  bool // false: the node could not be aligned; keep (row, col) as is
}

// visualPin resolves p's cell to a source rune at the current width.
func (t *transcript) visualPin(p visualPoint) visualPin {
	pin := visualPin{p: p}
	text, _, _, why := t.nodeQuoteSource(p.ref)
	if why != "" {
		return pin
	}
	rows := t.nodeRows(p.ref)
	if p.row >= len(rows) {
		return pin
	}
	aligned := alignRows(text, rows)
	if off, _, ok := colToRune(aligned[p.row], p.col, true); ok {
		pin.off, pin.ok = off, true
	}
	return pin
}

// visualUnpin finds the cell that stands for the pinned rune at the width
// the index now holds, or the nearest cell after it.
func (t *transcript) visualUnpin(pin visualPin) visualPoint {
	p := pin.p
	rows := t.nodeRows(p.ref)
	if len(rows) == 0 {
		return p
	}
	if !pin.ok {
		if p.row >= len(rows) {
			p.row = len(rows) - 1
		}
		return p
	}
	text, _, _, _ := t.nodeQuoteSource(p.ref)
	aligned := alignRows(text, rows)
	best, bestRow, bestCol := -1, p.row, p.col
	for r, a := range aligned {
		if !a.aligned {
			continue
		}
		for c, off := range a.cols {
			if off < pin.off {
				continue
			}
			if best < 0 || off < best {
				best, bestRow, bestCol = off, r, c
			}
			if off == pin.off {
				return visualPoint{ref: p.ref, row: r, col: c}
			}
		}
	}
	if bestRow >= len(rows) {
		bestRow = len(rows) - 1
	}
	return visualPoint{ref: p.ref, row: bestRow, col: bestCol}
}
