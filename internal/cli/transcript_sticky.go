package cli

import "github.com/jack-work/figaro/internal/livelog/aria"

// The sticky question: the rows of a turn's inquiry that have scrolled off the
// top, pinned above the body so a reader deep inside a long turn can still see
// what was asked. The rows are the block's own, composed by the path that draws
// it inline, so crossing a turn boundary is a continuation rather than a swap:
// what leaves the body at the top arrives in the header, and scrolling back up
// hands it back.

// stickyText is how many rows of the question the header shows; one more row
// carries the rule beneath it.
const stickyText = 2

// sticky reports whether the header is on.
func (t *transcript) sticky() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.sticky
}

// headRows is the chrome reserved above the body. It does not vary with scroll
// position: a header that changed height as the reader moved would change the
// body height, and with it the offset the header is derived from.
func (t *transcript) headRows() int {
	if !t.sticky() {
		return 0
	}
	return stickyText + 1
}

// stickyBlock is a turn's inquiry rows, composed as the transcript composes
// them inline.
func (t *transcript) stickyBlock(turn int) []transcriptRow {
	key := keyOf(aria.Message{Turn: turn})
	if rows, ok := t.stickyCache[key]; ok {
		return rows.rows
	}
	q, ok := t.client.InquiryOf(turn)
	if !ok {
		return nil
	}
	rows := t.renderMsgBase(aria.Message{
		Turn: turn, Inquiry: q.Text, InquirySegments: q.Segments, FormDeltas: q.FormDeltas,
	})
	if t.stickyCache == nil {
		t.stickyCache = map[sliceKey]cachedMessage{}
	}
	t.stickyCache[key] = rows
	return rows.rows
}

// stickyTurn is the turn owning the top row of the body, and how many of its
// inquiry rows lie above it. A gap carries no turn, so the row above it
// answers; below the first block the count is zero and nothing is pinned.
func (t *transcript) stickyTurn() (turn, above int) {
	k := t.index.entryAt(t.offset)
	for k >= 0 && t.index.entries[k].isGap() {
		k--
	}
	if k < 0 {
		return 0, 0
	}
	turn = t.index.entries[k].turn
	block := len(t.stickyBlock(turn))
	if block == 0 {
		return turn, 0
	}
	head, ok := t.headEntry(turn)
	if !ok {
		// Only the tail of the turn is held, so the whole question is above.
		return turn, block
	}
	above = t.offset - entryRowsStart(head)
	if above < 0 {
		above = 0
	}
	if above > block {
		above = block
	}
	return turn, above
}

// headEntry is the entry holding a turn's first node, which is the one that
// draws its question.
func (t *transcript) headEntry(turn int) (*lineEntry, bool) {
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.turn == turn && !e.isGap() && e.key.from() == 0 {
			return e, true
		}
	}
	return nil, false
}

// stickyLines is the header as painted: the last rows of the question to have
// left the body, and the rule under them.
func (t *transcript) stickyLines(hl string, sel selectionSpan) []string {
	if t.headRows() == 0 {
		return nil
	}
	out := make([]string, t.headRows())
	turn, above := t.stickyTurn()
	if above == 0 {
		return out
	}
	rows := t.stickyBlock(turn)
	lo := above - stickyText
	if lo < 0 {
		lo = 0
	}
	for i, r := range rows[lo:above] {
		out[stickyText-(above-lo)+i] = t.rowLine(r, hl, sel)
	}
	out[len(out)-1] = t.transRule()
	return out
}
