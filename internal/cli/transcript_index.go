package cli

import "github.com/jack-work/figaro/internal/livelog/aria"

// A line index over the retained message window.

// lineEntry is one message's contribution to the transcript's line space.
// start is the absolute line of the entry's first line: the separator's
// first blank when sep is set, otherwise the first row.
type lineEntry struct {
	turn int
	// key is the SLICE the lines belong to, not just its turn: a tall turn is
	// several entries, and the viewport anchor has to tell them apart or a page
	// landing that prepends an earlier slice of the SAME turn snaps the viewport
	// to that slice's top.
	key   sliceKey
	start int
	sep   bool // preceded by the ""/rule separator pair
	open  bool // the live/held-open message, re-rendered every frame
	rows  []transcriptRow
	// gap is the hole this entry stands for, when it stands for one. A gap
	// entry has NO rows: it renders as EXACTLY ONE row, synthesized per frame
	// because its text depends on the width. See transcript.gapRow.
	gap *aria.Gap
}

// isGap reports whether the entry is a hole rather than a message.
func (e *lineEntry) isGap() bool { return e.gap != nil }

// sepRows is the height of the separator between two messages: a blank, then
// the RULE, and the next message's voice header directly beneath it, with no
// gap.
const sepRows = 2

// sepHeight is how many lines THIS entry spends on its leading separator: the
// separator's height when it has one, zero otherwise.
func (e *lineEntry) sepHeight() int {
	if e.sep {
		return sepRows
	}
	return 0
}

// height is the number of lines the entry occupies, separator included.
func (e *lineEntry) height() int {
	if e.isGap() {
		return e.sepHeight() + 1
	}
	return e.sepHeight() + len(e.rows)
}

// refAt is the node a line of this entry belongs to, or the zero nodeRef for a
// line that belongs to no node: a separator row, a gap sentinel, or an
// out-of-range index.
func (e *lineEntry) refAt(rel int) nodeRef {
	rel -= e.sepHeight() // separator rows go negative: they belong to no node
	if rel < 0 || e.isGap() || rel >= len(e.rows) {
		return nodeRef{}
	}
	return e.rows[rel].ref
}

// lineIndex is the per-frame index. entries and scratch ping-pong so a rebuild
// can compare the new shape against the old one without allocating.
type lineIndex struct {
	entries []lineEntry
	scratch []lineEntry
	total   int
	// rev is the transcript's windowRev this index was built from: the single
	// authority on "the retained page set changed", shared with the page layer.
	// See transcript.invalidateWindow.
	rev uint64
}

// entryAt returns the index of the entry owning absolute line i, or -1.
func (x *lineIndex) entryAt(i int) int {
	if i < 0 || i >= x.total || len(x.entries) == 0 {
		return -1
	}
	lo, hi := 0, len(x.entries)
	for lo < hi { // first entry with start > i
		mid := int(uint(lo+hi) >> 1)
		if x.entries[mid].start <= i {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

// buildIndex refreshes the line index for the current retained window. It
// subsumes the bookkeeping lines() used to do: tail reset while following,
// width invalidation of rowCache, and keeping lineTurn current: but stops short
// of materializing any row text.
func (t *transcript) buildIndex() {
	if t.follow {
		t.resetToTail()
	}
	if t.cacheW != t.w { // width changed: cached rows are stale
		t.rowCache = map[sliceKey]cachedMessage{}
		t.cacheW = t.w
	}
	entries, total := t.index.scratch[:0], 0
	// ONE AUTHORITY ON HOW TALL AN ENTRY IS: lineEntry.height. Line space is
	// advanced by asking the entry, not by re-deriving it here: the two
	// disagreeing is how a gap could be one row in the index and several on
	// screen, which no frame golden would catch.
	// A SEPARATOR MARKS A TURN BOUNDARY, NOT AN ENTRY BOUNDARY. One turn can be
	// several entries, a long agentic turn is delivered in slices, and paging
	// back into it delivers more, and a rule between those slices says "another
	// exchange began here" when nothing did. That is what made a long turn read
	// as a run of turns each missing its question.
	prevTurn := -1
	add := func(turn int, key sliceKey, rows []transcriptRow, open bool, gap *aria.Gap) {
		sep := total > 0 && turn != prevTurn
		prevTurn = turn
		e := lineEntry{
			turn: turn, key: key, start: total, sep: sep,
			open: open, rows: rows, gap: gap,
		}
		entries = append(entries, e)
		total += e.height()
	}
	// GAP-AWARE, and the only consumer that is. The pager draws a hole rather
	// than closing over it, so it walks the window as SEGMENTS: runs, and the
	// holes between them. Everything else in the pager stays gap-blind (see
	// forEachMessage) and is simply told less.
	t.client.ForEachSegment(t.from, windowEnd, func(m aria.Message) bool {
		rows, ok := t.rowCache[keyOf(m)]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[keyOf(m)] = rows
		}
		add(m.Turn, keyOf(m), rows.rows, false, nil)
		return true
	}, func(g aria.Gap) bool {
		// The gap's key is its own first missing anchor, packed the way a slice
		// key is. It cannot collide with a held message's key (that anchor is
		// precisely what we do NOT hold), so the viewport anchor and the resize
		// restore keep working across a hole.
		hole := g
		add(0, gapKey(g), nil, false, &hole)
		return true
	})
	if open := t.openMessage(); open != nil {
		add(open.Turn, keyOf(*open), t.renderMsgBase(*open).rows, true, nil)
	}
	// The page set moved => the index describes a different window, full stop.
	// That is the one authority (windowRev); the shape diff below only has to
	// catch the things the page set cannot see, a width change, an expanded
	// tool, the open message growing a token.
	changed := t.index.rev != t.windowRev
	if !changed {
		changed = len(entries) != len(t.index.entries) || total != t.index.total
	}
	if !changed {
		for i := range entries {
			if entries[i].key != t.index.entries[i].key ||
				entries[i].start != t.index.entries[i].start ||
				len(entries[i].rows) != len(t.index.entries[i].rows) {
				changed = true
				break
			}
		}
	}
	t.index.scratch, t.index.entries, t.index.total = t.index.entries, entries, total
	t.index.rev = t.windowRev
	if changed {
		t.rebuildLineLT()
	}
}

// rebuildLineLT refills the slice-per-line map used for resize anchoring. Only
// called when the index shape actually changed, a scroll leaves it alone.
// Separator rows carry the FOLLOWING message's slice, as they always have.
func (t *transcript) rebuildLineLT() {
	if cap(t.lineKey) < t.index.total {
		t.lineKey = make([]sliceKey, t.index.total)
	}
	t.lineKey = t.lineKey[:t.index.total]
	for k := range t.index.entries {
		e := &t.index.entries[k]
		// Driven by height(), the one authority: separator, rows and the gap
		// sentinel alike. Counting e.rows here instead is how a gap entry ends
		// up with a lineKey hole that only resize anchoring can see.
		for i := e.start; i < e.start+e.height(); i++ {
			t.lineKey[i] = e.key
		}
	}
}

// gapKey is the sliceKey a hole is addressed by: its first MISSING anchor,
// packed exactly as a message's (Turn, From) is. It cannot collide with a held
// message: that anchor is the one we do not hold, and it moves when the hole
// shrinks, which is what makes the viewport anchor follow a fill.
func gapKey(g aria.Gap) sliceKey {
	return sliceKey(int64(g.From.Turn)<<sliceKeyFromBits | int64(g.From.Node&(1<<sliceKeyFromBits-1)))
}

// transRule lives in transcript.go, memoized per width there.

// forEachWindowRow walks absolute lines [a, b) and hands each to fn as the
// (entry, entry-relative row) it comes from.
func (t *transcript) forEachWindowRow(a, b int, fn func(e *lineEntry, rel int)) {
	if a < 0 {
		a = 0
	}
	if b > t.index.total {
		b = t.index.total
	}
	if a >= b {
		return
	}
	for k := t.index.entryAt(a); k >= 0 && k < len(t.index.entries); k++ {
		e := &t.index.entries[k]
		n := e.height()
		for rel := a - e.start; rel < n; rel++ {
			fn(e, rel)
			a++
			if a >= b {
				return
			}
		}
	}
}

// window materializes absolute lines [a, b) into dst (reusing its storage),
// applying selection decoration and search highlighting to those rows only.
// The rows themselves come out of rowCache already clipped and gutter-prefixed
// (C's plainNodeRow), so an undecorated, unhighlighted row costs a slice read
// and no allocation at all.
func (t *transcript) window(a, b int, dst []string) []string {
	dst = dst[:0]
	hl := t.activeHighlight()
	sel := t.selectionSpan()
	t.forEachWindowRow(a, b, func(e *lineEntry, rel int) {
		dst = append(dst, t.entryLine(e, rel, hl, sel))
	})
	return dst
}

// rowRefs collects the node each of absolute lines [a, b) belongs to, in the
// same order window materializes them: so index i of the two results describes
// one row: its text and the node it addresses.
func (t *transcript) rowRefs(a, b int, dst []nodeRef) []nodeRef {
	dst = dst[:0]
	t.forEachWindowRow(a, b, func(e *lineEntry, rel int) {
		dst = append(dst, e.refAt(rel))
	})
	return dst
}

// lineAt materializes a single absolute line. Search walks line space one
// candidate at a time through this, so a jump costs O(distance to the match)
// rather than materializing the whole retained window up front.
func (t *transcript) lineAt(i int) string {
	k := t.index.entryAt(i)
	if k < 0 {
		return ""
	}
	e := &t.index.entries[k]
	return t.entryLine(e, i-e.start, t.activeHighlight(), t.selectionSpan())
}

// entryLine renders line rel of an entry exactly as the whole-window
// materialization pass did: separator rows raw, node rows decorated then
// highlighted. Decoration happens HERE, at paint time, and not into rowCache:
// the cue is a function of the selection, which changes far more often than
// the rows do, so baking it into the cache would mean invalidating (and
// re-rendering prose for) every message the selection touches on every ^N.
func (t *transcript) entryLine(e *lineEntry, rel int, hl string, sel selectionSpan) string {
	if e.sep {
		// The one place the separator's rows are NAMED rather than counted:
		// a blank, then the rule. Everything else asks sepHeight().
		switch rel {
		case 0:
			return ""
		case 1:
			return t.transRule()
		}
	}
	rel -= e.sepHeight()
	if e.isGap() {
		// THE SEPARATOR PAIR ABOVE A GAP IS DELIBERATE, and it is the same pair
		// as anywhere else: the rule is the OVERLINE of what sits beneath it
		// (see sepRows). The sentinel row plays the part of the voice header for
		// the block that is missing, so the seam above a hole looks exactly like
		// the seam above a message: which is the point, because a hole IS where
		// messages would be.
		return t.gapRow(e.gap)
	}
	r := e.rows[rel]
	line := r.text
	if r.ref.valid() {
		// r.text is already in its plainNodeRow resting form, so this is a
		// no-op returning line untouched unless the row is actually selected.
		line = decorateNodeRow(line, sel.mark(r.ref), t.w)
	}
	if hl != "" {
		line = highlightMatches(line, hl)
	}
	return line
}

// selectionSpan is the O(1) form of selectionMarks: the ordered endpoints of
// the active selection, answering "is this ref selected/focused" by
// comparison instead of by building a map over every retained node.
type selectionSpan struct {
	active bool
	lo, hi selectionPoint
	focus  nodeRef
}

func (t *transcript) selectionSpan() selectionSpan {
	if !t.selection.active {
		return selectionSpan{}
	}
	lo, hi := t.selection.anchor, t.selection.focus
	if pointLess(hi, lo) {
		lo, hi = hi, lo
	}
	return selectionSpan{active: true, lo: lo, hi: hi, focus: t.selection.focus.nodeRef}
}

func (s selectionSpan) mark(ref nodeRef) selectionMark {
	if !s.active {
		return selectionMark{}
	}
	p := selectionPoint{nodeRef: ref}
	if pointLess(p, s.lo) || pointLess(s.hi, p) {
		return selectionMark{}
	}
	return selectionMark{selected: true, active: ref == s.focus}
}

// nodeSpanOf reports the line range a node occupies, derived from the index
// instead of from a map rebuilt over every materialized row. Duplicated LTs
// (a held-open message shadowing a committed one) widen the span, matching the
// old nodeRows accumulation.
func (t *transcript) nodeSpanOf(ref nodeRef) (nodeSpan, bool) {
	span, found := nodeSpan{}, false
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.turn != ref.turn {
			continue
		}
		base := e.start + e.sepHeight()
		for i := range e.rows {
			if e.rows[i].ref != ref {
				continue
			}
			if !found {
				span.first, found = base+i, true
			}
			span.last = base + i
		}
	}
	return span, found
}
