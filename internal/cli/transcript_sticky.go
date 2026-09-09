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

// stickyText is the most rows of a question the header holds.
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
	rows     []transcriptRow
	textLo   int // first row of the question's own text
	textHigh int // one past its last row
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
	// The deltas sit below the question, under a blank row of their own.
	q.textHigh = len(q.rows)
	if n := len(formDeltaLines(inq.FormDeltas, t.w, t.expanded[nodeRef{turn: turn, index: inquiryNode}])); n > 0 {
		q.textHigh -= n + 1
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

// stickyRows is the head of the question the reader is inside, held while any
// of that question's own text is above the top of the pane. It does not shrink
// as the block comes back: the conversation moves underneath it and the header
// simply lets go once the question can speak for itself.
func (t *transcript) stickyRows() []transcriptRow {
	if !t.sticky() {
		return nil
	}
	turn, above := t.stickyTurn()
	q := t.stickyBlockOf(turn)
	if q.empty() || above <= q.textLo {
		return nil
	}
	return q.rows[q.textLo:min(q.textLo+stickyText, q.textHigh)]
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
		if i == len(rows)-1 && q.textLo+len(rows) < q.textHigh {
			line = stickyClip(line, t.w)
		}
		out = append(out, line)
	}
	// The pinned question stands out of its place in the conversation, so it
	// carries its address the way ^O draws every other one.
	out[0] = ldrender.OverlayRight(out[0], term.Dim(coordLabel(turn, inquiryNode, 0, t.coordFormat())), t.w)
	if above >= len(q.rows) {
		out = append(out, t.transRule())
	}
	return out
}

// stickyClip marks a row as the last of the question the header could fit.
func stickyClip(line string, w int) string {
	return clipToWidth(strings.TrimRight(line, " "), w-len(stickyEllipsis)) + term.Dim(stickyEllipsis)
}
