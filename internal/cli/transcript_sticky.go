package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// The sticky question floats: the header covers the rows it stands on rather than
// pushing them down, so the geometry never changes when it appears or goes, and it
// goes exactly when the block draws the same rows in the same place.

// Rows 0 and 1 of the block itself: the heading and the first line of the question.
const stickyText = 2

const stickyEllipsis = " .."

func (t *transcript) sticky() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.sticky
}

// The body is not shortened by the header: see renderFrame.
func (t *transcript) headRows() int { return len(t.stickyLines("", selectionSpan{})) }

// stickyQuestion is the whole inline block plus the rows that are the question's
// own text.
type stickyQuestion struct {
	gutter   string
	rows     []transcriptRow
	textLo   int // first row of the question's own text
	textHigh int // one past its last row of text
}

func (q stickyQuestion) empty() bool { return q.textHigh <= q.textLo }

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
	sec := t.section(m, ldrender.BlockInquiry, livedoc.Node{}, inq.FormDeltas)
	sec.open = false
	q.gutter = buildAdornment(inquiryAdorner{}, sec, t.w).Gutter
	// The question's text stops where the adornment starts; its closing blank
	// row is not text either.
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

// Reads the entry the body starts in: a live turn's open suffix shadows the
// committed copy, so the turn's first entry would count the wrong rows.
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
		return turn, block
	}
	above = min(max(t.offset-entryRowsStart(e), 0), block)
	return turn, above
}

// The block's own rows 0 and 1, so when the header lets go the block draws the
// same rows in the same place.
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

// The rule is dropped while the block itself is still on screen, or it would cut
// the question in two.
func (t *transcript) stickyLines(hl string, sel selectionSpan) []string {
	rows := t.stickyRows()
	if len(rows) == 0 {
		return nil
	}
	turn, above := t.stickyTurn()
	q := t.stickyBlockOf(turn)
	out := make([]string, 0, len(rows)+1)
	for i, r := range rows {
		line := t.rowLine(r, hl, sel, -1)
		if i == len(rows)-1 && len(rows) < q.textHigh {
			line = stickyClip(line, t.w)
		}
		out = append(out, line)
	}
	out[0] = ldrender.OverlayColumn(out[0], 0, "∨")
	out[0] = ldrender.OverlayRight(out[0], term.Dim(coordLabel(turn, inquiryNode, 0, t.coordFormat())+" "), t.w-ldrender.GutterCols)
	out[0] = ldrender.OverlayGutter(out[0], q.gutter, t.w)
	if above >= len(q.rows) {
		// The header covers rows, so a gutter may run under it on either
		// side; the rule says which (joinRule).
		seen := len(out)
		out = append(out, joinRule(t.transRule(),
			t.stickyCut(q, rows, seen),
			t.lineCut(t.offset+seen+1, t.offset+seen)))
	}
	return out
}

// The gutter meeting the header's rule from above.
func (t *transcript) stickyCut(q stickyQuestion, rows []transcriptRow, seen int) gutterCut {
	cut := gutterCut{row: t.rowLine(rows[len(rows)-1], "", selectionSpan{}, -1)}
	if seen < len(q.rows) {
		cut.hidden = q.rows[seen].text
		cut.sameBlock = q.rows[seen].ref == rows[len(rows)-1].ref
	}
	return cut
}

// seen is the visible row's absolute line, hidden the line the chrome covers
// beyond the rule.
func (t *transcript) lineCut(seen, hidden int) gutterCut {
	ref := t.refAtLine(seen)
	return gutterCut{
		row:       t.lineAt(seen),
		hidden:    t.lineAt(hidden),
		sameBlock: ref.valid() && ref == t.refAtLine(hidden),
	}
}

func (t *transcript) refAtLine(i int) nodeRef {
	k := t.index.entryAt(i)
	if k < 0 {
		return nodeRef{}
	}
	e := &t.index.entries[k]
	return e.refAt(i - e.start)
}

type gutterCut struct {
	row       string
	hidden    string
	sameBlock bool
}

// A tool block's header row carries no gutter but the body under it is the same
// block, so sameBlock counts as going on.
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

// A stroke only where the line goes on past the rule; a gutter that merely begins
// or ends there is already whole.
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

// Only indentation may precede it: a vertical line inside the text is content.
func gutterColumn(row string) (int, bool) {
	rest, col := firstVisible(row)
	for _, glyph := range []string{"│", quoteOpen, quoteClose} {
		if strings.HasPrefix(rest, glyph) {
			return col, true
		}
	}
	return 0, false
}

// Leading escapes cost nothing, leading blanks one column each.
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

func stickyClip(line string, w int) string {
	return clipToWidth(strings.TrimRight(line, " "), w-len(stickyEllipsis)) + term.Dim(stickyEllipsis)
}
