package cli

import "github.com/jack-work/figaro/internal/livelog/aria"

// A line index over the retained message window.
//
// The transcript's line space is the concatenation of every retained message's
// rendered rows, with a ""/rule/"" separator triple injected between messages.
// Materializing that whole space costs O(retained rows) — tens of thousands of
// clipToWidth calls — yet a frame paints ~40 of them and a pure viewport move
// (j/k, wheel, u/d, gg/G) changes none of the content.
//
// lineIndex records, per message, only where its rows START in line space. It
// is rebuilt every frame, but the rebuild is O(#messages) map lookups and
// arithmetic (committed rows come straight out of rowCache), so it never
// touches row text. Absolute line -> row is then a binary search, and
// selection decoration plus search highlighting are applied lazily, only to
// the rows actually painted.

// lineEntry is one message's contribution to the transcript's line space.
// start is the absolute line of the entry's first line — the separator's
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
}

// sepRows is the height of the separator between two messages: a blank, then
// the RULE — and the next message's voice header directly beneath it, with no
// gap.
//
// THE RULE IS THE HEADER'S OVERLINE, not the previous message's underline.
// That is already the shape renderMsgBase draws inside a message, between a
// turn's question and the reply to it ("> input" / blank / text / blank / RULE
// / "< figaro"), and TestInquiryChromeAgreesAcrossViews pins it. The separator
// BETWEEN messages used to be a three-row triple — blank, rule, blank — so the
// same rule grew a trailing blank whenever it happened to fall on a message
// boundary, and "> input" sat one row lower than "< figaro" for no reason a
// reader could see.
//
// The asymmetry was a residue: the question used to be its own message, so both
// of its seams were message boundaries and both were loose. Once the inquiry
// became text on the turn (e7fb039) the seam below it moved inside a message
// and tightened, while the seam above it stayed behind.
const sepRows = 2

// sepHeight is how many lines THIS entry spends on its leading separator: the
// separator's height when it has one, zero otherwise.
//
// Every conversion between entry-relative and absolute line space goes through
// here rather than testing e.sep and naming the constant again. That is not
// tidiness. Of the places that encode the separator's height, only entryLine
// paints a row; the other two — rebuildLineLT's anchor fill and nodeSpanOf's
// span arithmetic — live in INDEX space, so when they disagree with the real
// height nothing on screen looks wrong. A stale value there instead puts the
// resize anchor and selection scroll-into-view one line further out of phase
// per preceding separator, degrading with distance down the transcript, and no
// frame golden can see it. Asking the entry beats naming the number.
func (e *lineEntry) sepHeight() int {
	if e.sep {
		return sepRows
	}
	return 0
}

// height is the number of lines the entry occupies, separator included.
func (e *lineEntry) height() int { return e.sepHeight() + len(e.rows) }

// lineIndex is the per-frame index. entries and scratch ping-pong so a rebuild
// can compare the new shape against the old one without allocating.
type lineIndex struct {
	entries []lineEntry
	scratch []lineEntry
	total   int
	// rev is the transcript's windowRev this index was built from — the single
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
// subsumes the bookkeeping lines() used to do — tail reset while following,
// width invalidation of rowCache, and keeping lineTurn current — but stops short
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
	add := func(m aria.Message, rows []transcriptRow, open bool) {
		sep := total > 0 // rule separator BETWEEN messages only
		entries = append(entries, lineEntry{
			turn: m.Turn, key: keyOf(m), start: total, sep: sep, open: open, rows: rows,
		})
		if sep {
			total += sepRows
		}
		total += len(rows)
	}
	// forEachMessage walks the pages in place: materializing the merged
	// message slice was 2 KB of garbage per frame.
	t.forEachMessage(func(m aria.Message) {
		rows, ok := t.rowCache[keyOf(m)]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[keyOf(m)] = rows
		}
		add(m, rows.rows, false)
	})
	if open := t.openMessage(); open != nil {
		add(*open, t.renderMsgBase(*open).rows, true)
	}
	// The page set moved => the index describes a different window, full stop.
	// That is the one authority (windowRev); the shape diff below only has to
	// catch the things the page set cannot see — a width change, an expanded
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
// called when the index shape actually changed — a scroll leaves it alone.
// Separator rows carry the FOLLOWING message's slice, as they always have.
func (t *transcript) rebuildLineLT() {
	if cap(t.lineKey) < t.index.total {
		t.lineKey = make([]sliceKey, t.index.total)
	}
	t.lineKey = t.lineKey[:t.index.total]
	for k := range t.index.entries {
		e := &t.index.entries[k]
		i := e.start
		for range e.sepHeight() {
			t.lineKey[i] = e.key
			i++
		}
		for range e.rows {
			t.lineKey[i] = e.key
			i++
		}
	}
}

// transRule lives in transcript.go, memoized per width there.

// window materializes absolute lines [a, b) into dst (reusing its storage),
// applying selection decoration and search highlighting to those rows only.
// The rows themselves come out of rowCache already clipped and gutter-prefixed
// (C's plainNodeRow), so an undecorated, unhighlighted row costs a slice read
// and no allocation at all.
func (t *transcript) window(a, b int, dst []string) []string {
	dst = dst[:0]
	if a < 0 {
		a = 0
	}
	if b > t.index.total {
		b = t.index.total
	}
	if a >= b {
		return dst
	}
	hl := t.activeHighlight()
	sel := t.selectionSpan()
	for k := t.index.entryAt(a); k >= 0 && k < len(t.index.entries); k++ {
		e := &t.index.entries[k]
		n := e.height()
		for rel := a - e.start; rel < n; rel++ {
			dst = append(dst, t.entryLine(e, rel, hl, sel))
			a++
			if a >= b {
				return dst
			}
		}
	}
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
	r := e.rows[rel]
	line := r.text
	if r.ref.valid() {
		// r.text is already in its plainNodeRow resting form, so this is a
		// no-op returning line untouched unless the row is actually selected.
		line = decorateNodeRow(line, sel.mark(r.ref))
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
