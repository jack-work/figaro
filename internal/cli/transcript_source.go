package cli

// render.Row carries no provenance, so the mapping from a rendered row to the
// source is recovered by walking source and rows together rune by rune: a row rune
// matching the next source rune (within a short lookahead) consumes it; one
// matching nothing nearby was inserted by the renderer; skipped source runes were
// syntax. Where alignment is lost the coordinate widens to the nearest aligned
// boundary and says so.

import (
	"strings"
	"unicode"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/sdk/aria"
	"github.com/mattn/go-runewidth"
)

type sourceRow struct {
	// cols[c] is the source rune index for visible column c, or -1 for an
	// inserted cell; wide runes repeat.
	cols []int
	// start and end are not to be trusted when aligned is false.
	start, end int
	aligned    bool
}

// Markdown syntax between two visible runes is short; further than this is an
// insertion, not a skip.
const alignLookahead = 48

const alignFloor = 0.6

// Single-rune hits are how a chrome row finds its letters scattered through a
// paragraph.
const alignRun = 4

// A row that does not align does not move the cursor, so chrome cannot push the
// next text row off its place.
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

func alignRow(src []rune, cursor int, plain string) (sourceRow, int) {
	sr := sourceRow{start: -1, end: -1}
	matched, tried, run, longest := 0, 0, 0, 0
	for _, r := range []rune(plain) {
		w := runeCells(r)
		hit := -1
		if !unicode.IsSpace(r) {
			tried++
			// Source whitespace between two visible runes is folded,
			// not deleted: not a break in the run.
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

// base is the rune offset of text within the block (a clamped tool tail starts
// partway in).
func (t *transcript) nodeQuoteSource(ref nodeRef) (text string, at quote.Coord, base int, why string) {
	if ref.meta > 0 {
		return "", quote.Coord{}, 0, "metadata is not quotable: it is about the log, not something anyone said"
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
		// The rows are the output's tail when folded, and the producer
		// already cut its own head: base is both distances.
		src := n.Src[len(n.Src)-1]
		raw := strings.TrimRight(n.Output, "\n")
		shown, _ := tailOutput(raw, t.toolCap(ref))
		base = n.OutputBase + len([]rune(raw)) - len([]rune(shown))
		return shown, quote.Coord{LT: src.LT, Block: src.Block}, base, ""
	default:
		if len(n.Src) == 0 {
			return "", quote.Coord{}, 0, "this block carries no log coordinate"
		}
		return n.Markdown, quote.Coord{LT: n.Src[0].LT, Block: n.Src[0].Block}, 0, ""
	}
}

// Walks from the turn's anchor, not the window's start.
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

func (t *transcript) toolCap(ref nodeRef) int {
	if t.expanded[ref] {
		return nodeOutputUnlimited
	}
	return nodeBashCapDefault
}

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

// note is set when the range was widened past an unaligned row.
func (t *transcript) visualRange() (r quote.Range, note string, err string) {
	if !t.visual.highlighted() {
		return quote.Range{}, "", "no highlight (v or V at the cursor to start one)"
	}
	// An end evicted from the retained window is a refusal, not a guess;
	// scrolling up pages it back in.
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

// Start: the first rune the cell stands for; end: one past the last. widened: the
// offset came from a neighbour row.
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
		// Char mode: an inserted cell snaps to the next (start) or
		// previous (end) real rune.
		if off, inclusive, ok := colToRune(row, p.col, isStart); ok {
			if isStart || !inclusive {
				return at, base + off, false, ""
			}
			return at, base + off + 1, false, ""
		}
	}
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

// An end in the indent before a row's first rune names that rune as the exclusive
// bound, so the row contributes nothing rather than everything.
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

func (t *transcript) visualCoordinate() (token, note, err string) {
	r, note, err := t.visualRange()
	if err != "" {
		return "", "", err
	}
	return quote.Format(r), note, ""
}

// A visualPoint is (node, row, column) at one width; across a resize it is pinned
// to a source rune and found again.

type visualPin struct {
	p   visualPoint
	off int  // source rune offset the cell stood for
	ok  bool // false: the node could not be aligned; keep (row, col)
}

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
