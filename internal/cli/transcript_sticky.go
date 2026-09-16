package cli

import (
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

// The sticky question: the head of a turn's question, held over the top of the
// conversation while the reader is inside the answer to it. The rows are the
// block's own, composed by the path that draws it inline, and no rule stands
// between them and what follows.
//
// It FLOATS: the conversation scrolls underneath at its own pace, one row per
// keystroke, and the header covers the rows it stands on rather than pushing
// them down. So the geometry never changes when it appears or goes, and the
// moment it goes is the moment the question's own head has reached the top,
// where it draws the same rows in the same place.

// stickyText is how many of the block's own head rows the header holds: the
// heading that names the sender, and the first line of the question under it.
// They are rows 0 and 1 of the block, which is what makes the float seamless:
// see stickyRows.
const stickyText = 2

// stickyEllipsis marks a question the header could not show whole.
const stickyEllipsis = " .."

// sticky reports whether the header is on.
func (t *transcript) sticky() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.sticky
}

// headRows is how many rows of the conversation the header stands on, its rule
// included. The body is not shortened by it: see renderFrame.
func (t *transcript) headRows() int { return len(t.stickyLines("", selectionSpan{})) }

// stickyQuestion is a turn's question as the transcript composes it inline:
// every row of the block, and the half of them that is the question itself
// rather than the voice header above it or the form deltas below.
type stickyQuestion struct {
	gutter   string
	rows     []transcriptRow
	textLo   int // first row of the question's own text
	textHigh int // one past its last row of text
}

func (q stickyQuestion) empty() bool { return q.textHigh <= q.textLo }

// stickyBlockOf composes a turn's question block. The turn index rides the
// attribution, which is the row naming who asked.
func (t *transcript) stickyBlockOf(turn int) stickyQuestion {
	key := keyOf(aria.Message{Turn: turn})
	if q, ok := t.stickyCache[key]; ok {
		return q
	}
	var q stickyQuestion
	inq, ok := t.client.InquiryOf(turn)
	if !ok {
		return q
	}
	m := aria.Message{
		Turn: turn, Inquiry: inq.Text, InquirySegments: inq.Segments, FormDeltas: inq.FormDeltas,
	}
	q.rows = t.renderMsgBase(m).rows
	q.gutter = buildAdornment(inquiryAdorner{}, inq.FormDeltas, t.w, false).Gutter
	// The question's own text stops where its adornment starts: the first row
	// the snake touches (see adornment.go). The blank row the question closes
	// with is not text either, so a one-line question is not reported as
	// having more to show.
	q.textHigh = len(q.rows)
	for i, r := range q.rows {
		if r.spine.Kind != ldrender.SpineNone {
			q.textHigh = i
			break
		}
	}
	for q.textHigh > 0 && !q.rows[q.textHigh-1].ref.valid() {
		q.textHigh--
	}
	for i, r := range q.rows {
		if r.ref.valid() {
			q.textLo = i
			break
		}
	}
	if q.textHigh > len(q.rows) {
		q.textHigh = len(q.rows)
	}
	if t.stickyCache == nil {
		t.stickyCache = map[sliceKey]stickyQuestion{}
	}
	t.stickyCache[key] = q
	return q
}

// stickyTurn is the turn owning the top row of the body, and how many rows of
// its block lie above it. It reads the entry the body actually starts in: one
// turn can hold several entries, and a live turn's open suffix shadows the
// committed copy, so a search for the turn's first entry would count against
// rows that are not the ones on screen.
func (t *transcript) stickyTurn() (turn, above int) {
	k := t.index.entryAt(t.offset)
	for k >= 0 && t.index.entries[k].isGap() {
		k--
	}
	if k < 0 {
		return 0, 0
	}
	e := &t.index.entries[k]
	turn = e.turn
	block := len(t.stickyBlockOf(turn).rows)
	if block == 0 {
		return turn, 0
	}
	if e.key.from() != 0 {
		// A continuation slice: the whole question is above the body.
		return turn, block
	}
	above = min(max(t.offset-entryRowsStart(e), 0), block)
	return turn, above
}

// stickyRows is the head of the question the reader is inside: the heading
// that names who asked, and the first line they wrote. They are the block's
// OWN rows 0 and 1, so the moment the header lets go -- the moment nothing of
// the block is above the body -- the block draws the same two rows in the same
// place, and the reader sees no change at all.
func (t *transcript) stickyRows() []transcriptRow {
	if !t.sticky() {
		return nil
	}
	turn, above := t.stickyTurn()
	q := t.stickyBlockOf(turn)
	if q.empty() || above <= 0 {
		return nil
	}
	rows := make([]transcriptRow, 0, stickyText)
	for _, r := range q.rows[:min(stickyText, q.textHigh)] {
		if len(rows) > 0 {
			r.mark = "" // the turn number lives on the heading row
		}
		rows = append(rows, r)
	}
	return rows
}

// stickyLines is the header as painted: the head of the question, its address
// against the right edge, a mark on the last row when the question goes on past
// what the header holds, and a rule closing it off from the conversation. The
// rule is dropped while the block itself is still on screen, where a line
// between the header and the rest of the same question would cut it in two.
func (t *transcript) stickyLines(hl string, sel selectionSpan) []string {
	rows := t.stickyRows()
	if len(rows) == 0 {
		return nil
	}
	turn, above := t.stickyTurn()
	q := t.stickyBlockOf(turn)
	out := make([]string, 0, len(rows)+1)
	for i, r := range rows {
		line := t.rowLine(r, hl, sel)
		if i == len(rows)-1 && len(rows) < q.textHigh {
			line = stickyClip(line, t.w)
		}
		out = append(out, line)
	}
	// The pinned question stands out of its place in the conversation, so it
	// carries its address the way ^O draws every other one.
	out[0] = ldrender.OverlayColumn(out[0], 0, "∨")
	out[0] = ldrender.OverlayRight(out[0], term.Dim(coordLabel(turn, inquiryNode, 0, t.coordFormat())+" "), t.w-ldrender.GutterCols)
	out[0] = ldrender.OverlayGutter(out[0], q.gutter, t.w)
	if above >= len(q.rows) {
		// The header COVERS rows rather than pushing them down, so a gutter
		// may run under it on either side. The rule says which: see joinRule.
		seen := len(out)
		out = append(out, joinRule(t.transRule(),
			t.stickyCut(q, rows, seen),
			t.lineCut(t.offset+seen+1, t.offset+seen)))
	}
	return out
}

// stickyCut is the gutter meeting the header's rule FROM ABOVE: the last
// pinned row carries one, and the question goes on under the rule.
func (t *transcript) stickyCut(q stickyQuestion, rows []transcriptRow, seen int) gutterCut {
	cut := gutterCut{row: t.rowLine(rows[len(rows)-1], "", selectionSpan{})}
	if seen < len(q.rows) {
		cut.hidden = q.rows[seen].text
		cut.sameBlock = q.rows[seen].ref == rows[len(rows)-1].ref
	}
	return cut
}

// lineCut is the gutter meeting a rule from the side the reader can see, where
// `seen` is that row's absolute line and `hidden` the line the chrome covers
// on the far side of the rule.
func (t *transcript) lineCut(seen, hidden int) gutterCut {
	ref := t.refAtLine(seen)
	return gutterCut{
		row:       t.lineAt(seen),
		hidden:    t.lineAt(hidden),
		sameBlock: ref.valid() && ref == t.refAtLine(hidden),
	}
}

// refAtLine is the block an absolute line belongs to, or the zero ref for
// chrome, a separator or a gap.
func (t *transcript) refAtLine(i int) nodeRef {
	k := t.index.entryAt(i)
	if k < 0 {
		return nodeRef{}
	}
	e := &t.index.entries[k]
	return e.refAt(i - e.start)
}

// gutterCut is one side of a rule: the row the reader SEES there, plus what
// the chrome hides immediately beyond it.
type gutterCut struct {
	row       string
	hidden    string
	sameBlock bool
}

// column answers where this side's gutter meets the rule, and whether the line
// GOES ON past it: the hidden row draws the same gutter, or belongs to the
// same block. The second half is what a tool block needs -- its header row
// carries no gutter, but the body under it is the same block, so a rule
// between them cuts one line, not two.
func (c gutterCut) column() (int, bool) {
	col, ok := gutterColumn(c.row)
	if !ok {
		return 0, false
	}
	if c.sameBlock {
		return col, true
	}
	other, ok := gutterColumn(c.hidden)
	return col, ok && other == col
}

// joinRule marks a rule where a block's vertical gutter is CUT by it. A stroke
// says the line goes on past the rule on that side and nothing else: up when
// the gutter above continues under it, down when the gutter below comes from
// above it, ┼ where one line does both. A gutter that merely begins or ends
// against the rule is a whole line already, and a junction there would branch
// to a block that is not there.
func joinRule(rule string, above, below gutterCut) string {
	up, upOK := above.column()
	down, downOK := below.column()
	w := displayWidth(rule)
	upOK = upOK && up < w
	downOK = downOK && down < w
	switch {
	case upOK && downOK && up == down:
		return ldrender.OverlayColumn(rule, up, "┼")
	case upOK && downOK:
		return ldrender.OverlayColumn(ldrender.OverlayColumn(rule, up, "┴"), down, "┬")
	case upOK:
		return ldrender.OverlayColumn(rule, up, "┴")
	case downOK:
		return ldrender.OverlayColumn(rule, down, "┬")
	}
	return rule
}

// gutterColumn is the column of a row's leading vertical rule, if it has one.
// Only indentation may precede it: a vertical line inside the text is content,
// not a connection to the chrome.
func gutterColumn(row string) (int, bool) {
	rest, col := firstVisible(row)
	if strings.HasPrefix(rest, "│") {
		return col, true
	}
	return 0, false
}

// firstVisible is a row from its first painted cell, and that cell's column:
// leading escapes cost nothing and leading blanks cost one each. It is how the
// chrome reads a row it did not compose.
func firstVisible(row string) (string, int) {
	col := 0
	for i := 0; i < len(row); {
		switch row[i] {
		case '\x1b':
			i, _ = escapeEnd(row, i)
		case ' ':
			col++
			i++
		default:
			return row[i:], col
		}
	}
	return "", col
}

// stickyClip marks a row as the last of the question the header could fit.
func stickyClip(line string, w int) string {
	return clipToWidth(strings.TrimRight(line, " "), w-len(stickyEllipsis)) + term.Dim(stickyEllipsis)
}
