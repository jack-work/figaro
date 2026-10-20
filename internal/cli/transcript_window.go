package cli

import (
	"github.com/jack-work/figaro/sdk/aria"
)

const (
	seekForward  = 1
	seekBackward = -1
)

type transcriptPageRequest struct {
	at    aria.Anchor
	limit int
	seek  int
	jump  *transcriptJump
	fill  *aria.Gap
}

const (
	transcriptPageSize    = 30
	transcriptMinPageSize = 6
	transcriptPageLimit   = 3
	transcriptTailLimit   = 2 * transcriptPageSize
)

// A var so the geometry sweep in transcript_geometry_bench_test.go can measure the
// tradeoff.
var transcriptWindowRows = 2400

// heldWindow reads the line index, which already computes both counts per frame.
func (t *transcript) heldWindow() (rows, messages int) {
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.open {
			continue
		}
		rows += e.height()
		messages++
	}
	return rows, messages
}

func (t *transcript) avgRowsPerMessage() int {
	rows, n := t.heldWindow()
	if n == 0 {
		return 0
	}
	if avg := rows / n; avg > 0 {
		return avg
	}
	return 1
}

func pageRowBudget() int { return transcriptWindowRows / transcriptPageLimit }

func (t *transcript) pageMessages() int {
	avg := t.avgRowsPerMessage()
	if avg <= 0 {
		return transcriptPageSize
	}
	n := pageRowBudget() / avg
	if n > transcriptPageSize {
		n = transcriptPageSize
	}
	if n < transcriptMinPageSize {
		n = transcriptMinPageSize
	}
	return n
}

// Cold, it starts at the floor rather than the ceiling: tuneTail grows the window
// into the budget, invisibly, since the added rows are above a viewport pinned to
// the tail.
func (t *transcript) tailKeep() int {
	if t.tailWant > 0 {
		return t.tailWant
	}
	return transcriptMinPageSize
}

// Two screens of runway: at wheel speed a few hundred milliseconds, enough for a
// local daemon round trip.
const transcriptPrefetchScreens = 2

func (t *transcript) wantOlder() bool {
	if t.search != nil {
		return true
	}
	return t.offset < transcriptPrefetchScreens*t.h
}

func (t *transcript) atAriaFloor() bool {
	if t.client.Count() == 0 {
		return !t.client.MoreBefore()
	}
	if t.from.Turn <= 1 && t.from.Node == 0 {
		return true
	}
	if _, held := t.client.Before(t.from, 1); held > 0 {
		return false
	}
	return !t.client.MoreBefore()
}

func (t *transcript) growWindow(n int) []aria.Message {
	if n <= 0 {
		return nil
	}
	floor, held := t.client.Before(t.from, n)
	if held == 0 {
		return nil
	}
	gained := make([]aria.Message, 0, held)
	t.client.ForEachIn(floor, t.from.Prev(), func(m aria.Message) bool {
		gained = append(gained, m)
		return true
	})
	t.lowerFloor(floor)
	return gained
}

// Never raises the floor: that is resetToTail's job, and only while following.
func (t *transcript) lowerFloor(a aria.Anchor) {
	if !a.Less(t.from) {
		return
	}
	t.from = a
	t.invalidateWindow()
}

// At the floor a search that found nothing goes home and a jump resolves against
// the beginning.
func (t *transcript) reachedFloor() {
	t.wantTop = false
	t.finishSearch(false)
	t.jumpAdvance()
	t.render()
}

func (t *transcript) pageCursor() (transcriptPageRequest, bool) {
	if !t.active {
		return transcriptPageRequest{}, false
	}
	if t.jump != nil {
		if at, dir, ok := t.jumpSeek(); ok {
			return transcriptPageRequest{at: at, seek: dir, jump: t.jump}, true
		}
		return transcriptPageRequest{}, false
	}
	for t.wantOlder() {
		anchor, within := t.viewportAnchor()
		before := t.from
		gained := t.growWindow(t.pageMessages())
		if len(gained) == 0 {
			break
		}
		t.absorbOlder(gained, anchor, within)
		if !t.from.Less(before) {
			break
		}
	}
	if gap := t.gapNear(); gap != nil {
		hole := *gap
		return transcriptPageRequest{fill: &hole}, true
	}
	if !t.wantOlder() {
		return transcriptPageRequest{}, false
	}
	if t.atAriaFloor() {
		t.reachedFloor()
		return transcriptPageRequest{}, false
	}
	return transcriptPageRequest{at: t.from, limit: t.pageMessages()}, true
}

func (t *transcript) absorbOlder(gained []aria.Message, anchor sliceKey, within int) {
	if t.search != nil {
		if t.findPage(t.search.query, gained) {
			t.search = nil
		}
		return
	}
	t.buildIndex()
	if t.wantTop {
		// Hold at the top of what is now held; the floor clears wantTop.
		t.offset = 0
		if t.atAriaFloor() {
			t.wantTop = false
		}
	} else {
		t.restoreViewportAnchor(anchor, within)
	}
	t.jumpAdvance()
}

func (t *transcript) applyPage(req transcriptPageRequest, page aria.Page) {
	if !t.active {
		return
	}
	anchor, within := sliceKey(0), 0
	if t.search == nil {
		anchor, within = t.viewportAnchor()
	}
	if req.seek != 0 {
		t.applySeek(req, page)
		return
	}
	t.client.SetMoreBefore(page.More.Before)
	msgs := t.client.Apply(page, aria.Quiet)
	if len(msgs) == 0 {
		// Nothing on the wire reports where an aria begins: a read that
		// hands back nothing new is the floor.
		t.client.SetMoreBefore(false)
		t.reachedFloor()
		return
	}
	// A page that does not lower the floor ends the walk, whatever the wire
	// says about more.
	before := t.from
	t.lowerFloor(anchorOf(msgs[0]))
	if t.from == before {
		t.client.SetMoreBefore(false)
		t.reachedFloor()
		return
	}
	t.absorbOlder(msgs, anchor, within)
	if t.search != nil {
		return // still walking
	}
	t.render()
}

func (t *transcript) applySeek(req transcriptPageRequest, page aria.Page) {
	t.client.Apply(page, aria.Quiet)
	t.invalidateWindow()
	if t.jump == nil || req.jump != t.jump {
		return
	}
	if len(page.Parts) == 0 {
		if t.jump.target.start {
			t.client.SetMoreBefore(false)
			t.settle()
			t.jumpAdvance()
			if t.jump == nil {
				t.render()
				return
			}
		}
		t.abandonJump(t.jump.target.missing())
		t.render()
		return
	}
	from, _ := page.Span()
	if !page.More.Before && !t.from.Less(from) {
		t.client.SetMoreBefore(false)
	}
	t.lowerFloor(from)
	j := t.jump
	if j.target.travel != 0 || j.target.start {
		part := page.Parts[0]
		if j.target.travel < 0 {
			part = page.Parts[len(page.Parts)-1]
		}
		if j.target.travel > 0 && int(part.ID) <= j.target.turn ||
			j.target.travel < 0 && int(part.ID) >= j.target.turn {
			t.abandonJump(j.target.missing())
			t.render()
			return
		}
		j.target = jumpTarget{turn: int(part.ID)}
		j.asked = nil
	}
	t.settle()
	t.jumpAdvance()
	if t.jump != nil && t.jump.asked != nil {
		t.abandonJump(t.jump.target.missing())
	}
	t.render()
}

func anchorOf(m aria.Message) aria.Anchor {
	return aria.Anchor{Turn: uint64(m.Turn), Node: m.From}
}

func pageMessages(p aria.Page) []aria.Message {
	return aria.NewClient().Apply(p, aria.Quiet)
}

// sliceKey packs turn (high bits) and node offset (low 20) into one integer: a
// two-word key measurably slowed the per-frame row cache. 2^20 nodes in a turn is
// not reachable.
type sliceKey int64

const sliceKeyFromBits = 20

func keyOf(m aria.Message) sliceKey { return sliceKeyOf(m.Turn, m.From) }

func sliceKeyOf(turn int, from uint64) sliceKey {
	return sliceKey(int64(turn)<<sliceKeyFromBits | int64(from))
}

func (k sliceKey) turn() int { return int(k >> sliceKeyFromBits) }

func (k sliceKey) from() uint64 { return uint64(k) & (1<<sliceKeyFromBits - 1) }

// Recomputes one anchor; Store.TailFrom walks backward, so this costs the window's
// size, not the aria's.
func (t *transcript) resetToTail() {
	t.wantTop = false
	keep := t.tailKeep()
	n := t.client.Count()
	if keep > n {
		keep = n
	}
	from, held := t.client.TailFrom(keep)
	if !held {
		// Nothing closed is retained (a cold join to a long turn): the
		// zero anchor would read as the tail, so anchor on the open
		// message.
		if open := t.openMessage(); open != nil {
			from = anchorOf(*open)
		}
	}
	if t.from == from {
		return
	}
	t.from = from
	t.invalidateWindow()
	t.tailTuned = false
	t.evictStale()
	t.pruneCaches()
}

// transcriptRetainRows bounds what the store holds behind the window: the return
// trip's worth, four windows.
var transcriptRetainRows = 4 * transcriptWindowRows

// A message is anywhere from 4 rows to 400, so a message count is not a memory bound.
func (t *transcript) retainMessages() int {
	avg := t.avgRowsPerMessage()
	if avg <= 0 {
		return transcriptTailLimit
	}
	n := transcriptRetainRows / avg
	if n < transcriptTailLimit {
		n = transcriptTailLimit
	}
	if max := transcriptPageSize * transcriptPageLimit * 4; n > max {
		n = max
	}
	return n
}

// evictStale never evicts what the window shows.
func (t *transcript) evictStale() {
	floor, ok := t.client.TailFrom(t.retainMessages())
	if !ok {
		return
	}
	if t.from.Less(floor) {
		floor = t.from
	}
	t.client.EvictBefore(floor)
}

// tuneTail grows a window that does not fill its budget (or the viewport) and
// shrinks one that overshot; reports whether the window changed.
func (t *transcript) tuneTail() bool {
	if t.tailTuned || !t.follow {
		return false
	}
	_, have := t.heldWindow()
	if have == 0 {
		return false
	}
	budget := pageRowBudget()
	fits, rows := t.tailFit(budget)

	// Overshot: the cut is a fact about measured heights, not a prediction
	// from an average, so re-deriving it converges.
	if fits < have {
		t.tailWant = fits
		t.tailTuned = true // the cut is the fixed point
		before := t.from
		t.resetToTail()
		t.tailTuned = true // resetToTail cleared it; the cut still stands
		return t.from != before
	}

	// Under budget: the average is a hint for how far to jump, and nowhere
	// else the control law.
	full := rows >= budget || have >= transcriptPageSize
	if full && t.index.total >= t.h {
		t.tailWant = have
		t.tailTuned = true
		return false
	}
	want := have + max(t.pageMessages(), 1)
	before := t.from
	t.tailWant = want
	t.resetToTail()
	if t.from == before {
		t.tailTuned = true // the whole aria is held
		return false
	}
	return true
}

func (t *transcript) tailFit(budget int) (messages, rows int) {
	for k := len(t.index.entries) - 1; k >= 0; k-- {
		e := &t.index.entries[k]
		if e.open {
			continue
		}
		h := max(e.height(), 1)
		if messages > 0 && rows+h > budget {
			break
		}
		rows += h
		messages++
	}
	return messages, rows
}

// The one authority on "the retained window changed".
func (t *transcript) invalidateWindow() { t.windowRev++ }

func (t *transcript) pruneCaches() {
	// Once per frame while following: the turn set is reused.
	keep := t.keepBuf
	if keep == nil {
		keep = make(map[int]bool, transcriptPageSize*transcriptPageLimit)
		t.keepBuf = keep
	} else {
		clear(keep)
	}
	// Rows follow the store, not the window: scrolling back over a message the
	// store still holds must cost neither I/O nor a re-render.
	t.client.ForEachIn(aria.Anchor{}, windowEnd, func(m aria.Message) bool {
		keep[m.Turn] = true
		return true
	})
	// The open turn is the live suffix, not in the store's window; pruning it
	// loses `expanded`, which is user state, on every following frame.
	if open := t.openMessage(); open != nil {
		keep[open.Turn] = true
	}
	for k := range t.rowCache {
		if !keep[k.turn()] {
			delete(t.rowCache, k)
		}
	}
	for _, state := range []map[nodeRef]bool{t.expanded, t.metaOpen} {
		for ref := range state {
			if !keep[ref.turn] {
				delete(state, ref)
			}
		}
	}
	// Counting what was deleted would cost as much as the bump.
	t.rowRev++
}

// On the frame path: no slice of the window is materialized.
func (t *transcript) forEachMessage(fn func(aria.Message)) {
	t.client.ForEachIn(t.from, windowEnd, func(m aria.Message) bool {
		fn(m)
		return true
	})
}

var windowEnd = aria.Anchor{Turn: ^uint64(0), Node: ^uint64(0)}

func (t *transcript) retained() (msgs, rows int) {
	if t.client != nil {
		msgs = t.client.Count()
	}
	for _, c := range t.rowCache {
		rows += len(c.rows)
	}
	return msgs, rows
}

// settle converges the tail window on the row budget; stopFollowing must not
// freeze an unconverged window.
func (t *transcript) settle() {
	t.buildIndex()
	for range 3 {
		if !t.tuneTail() {
			break
		}
		t.buildIndex()
	}
}

// A sentinel about to come on screen is a fetch already late: the same prefetch
// distance the floor uses.
func (t *transcript) gapNear() *aria.Gap {
	t.buildIndex()
	body, _ := t.layout(len(t.footLines()))
	lo := t.offset - transcriptPrefetchScreens*t.h
	hi := t.offset + body + transcriptPrefetchScreens*t.h
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if !e.isGap() {
			continue
		}
		if e.start >= lo && e.start <= hi {
			return e.gap
		}
	}
	return nil
}

// whole: no hole inside the window and nothing below its floor; the footer's total
// must not claim to be the conversation's size.
func (t *transcript) whole() bool {
	for k := range t.index.entries {
		if t.index.entries[k].isGap() {
			return false
		}
	}
	return t.atAriaFloor()
}
