package cli

import (
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/term"
)

// The sticky question: the rows of a turn's question that have scrolled off the
// top, pinned above the body. The rows are the block's own, composed by the
// path that draws it inline, and only rows the body is not showing are pinned,
// so the header and the block below it read as one thing.

// stickyText is how many rows of the question the header shows; one more row
// carries the rule beneath it.
const stickyText = 2

// stickyEllipsis marks a question the header could not show whole.
const stickyEllipsis = " .."

// sticky reports whether the header is on.
func (t *transcript) sticky() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.sticky
}

// headRows is the chrome reserved above the body. It does not vary with scroll
// position: a header that changed height as the reader moved would change the
// body height, and with it the offset it is derived from.
func (t *transcript) headRows() int {
	if !t.sticky() {
		return 0
	}
	return stickyText + 1
}

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

// stickyLines is the header as painted: the head of the question, and the rule
// under it. The head and not the rows nearest the body, because a question cut
// at an arbitrary row and continued below a rule reads as two broken
// paragraphs rather than as one question the reader is inside.
func (t *transcript) stickyLines(hl string, sel selectionSpan) []string {
	if t.headRows() == 0 {
		return nil
	}
	out := make([]string, t.headRows())
	turn, above := t.stickyTurn()
	if above == 0 {
		return out
	}
	q := t.stickyBlockOf(turn)
	if q.empty() {
		return out
	}
	high := min(q.textLo+stickyText, q.textHigh)
	// The header may only show what the body no longer does, so it waits until
	// the rows it would pin have left.
	if above < high {
		return out
	}
	rows := q.rows[q.textLo:high]
	for i, r := range rows {
		line := t.rowLine(r, hl, sel)
		if i == len(rows)-1 && high < q.textHigh {
			line = stickyClip(line, t.w)
		}
		out[stickyText-len(rows)+i] = line
	}
	out[len(out)-1] = t.transRule()
	return out
}

// stickyClip marks a row as the last of the question the header could fit.
func stickyClip(line string, w int) string {
	return clipToWidth(strings.TrimRight(line, " "), w-len(stickyEllipsis)) + term.Dim(stickyEllipsis)
}

// stickyStarts are the line-space positions of every question the window
// holds, in reading order: what a jump between questions travels between.
func (t *transcript) stickyStarts() []int {
	var out []int
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.isGap() || e.key.from() != 0 || t.stickyBlockOf(e.turn).empty() {
			continue
		}
		if start := entryRowsStart(e); len(out) == 0 || out[len(out)-1] != start {
			out = append(out, start)
		}
	}
	return out
}

// stickyJump moves the viewport to the question before or after the one the
// reader is inside. It is what ^N/^P mean while the header is up: with the
// question pinned, the unit of travel is the exchange rather than the node.
func (t *transcript) stickyJump(dir int) {
	t.settle()
	starts := t.stickyStarts()
	if len(starts) == 0 {
		return
	}
	t.stopFollowing()
	t.wantTop = false
	target := -1
	if dir < 0 {
		for _, s := range starts {
			if s < t.offset {
				target = s
			}
		}
	} else {
		for _, s := range starts {
			if s > t.offset {
				target = s
				break
			}
		}
	}
	if target < 0 {
		return
	}
	t.offset = target
	if _, maxOff := t.layout(len(t.footLines())); t.offset > maxOff {
		t.offset = maxOff
	}
}
