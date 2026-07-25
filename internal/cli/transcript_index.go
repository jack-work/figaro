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
	lt    int
	start int
	sep   bool // preceded by the ""/rule/"" separator triple
	open  bool // the live/held-open message, re-rendered every frame
	rows  []transcriptRow
}

// height is the number of lines the entry occupies, separator included.
func (e *lineEntry) height() int {
	if e.sep {
		return len(e.rows) + 3
	}
	return len(e.rows)
}

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
// width invalidation of rowCache, and keeping lineLT current — but stops short
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
	add := func(lt int, rows []transcriptRow, open bool) {
		sep := total > 0 // rule separator BETWEEN messages only
		entries = append(entries, lineEntry{lt: lt, start: total, sep: sep, open: open, rows: rows})
		if sep {
			total += 3
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
		add(m.LT, rows.rows, false)
	})
	if open := t.openMessage(); open != nil {
		add(open.LT, t.renderMsgBase(*open).rows, true)
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
			if entries[i].lt != t.index.entries[i].lt ||
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

// rebuildLineLT refills the LT-per-line map used for resize anchoring. Only
// called when the index shape actually changed — a scroll leaves it alone.
// Separator rows carry the FOLLOWING message's LT, as they always have.
func (t *transcript) rebuildLineLT() {
	if cap(t.lineLT) < t.index.total {
		t.lineLT = make([]int, t.index.total)
	}
	t.lineLT = t.lineLT[:t.index.total]
	for k := range t.index.entries {
		e := &t.index.entries[k]
		i := e.start
		if e.sep {
			t.lineLT[i], t.lineLT[i+1], t.lineLT[i+2] = e.lt, e.lt, e.lt
			i += 3
		}
		for range e.rows {
			t.lineLT[i] = e.lt
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
		switch rel {
		case 0, 2:
			return ""
		case 1:
			return t.transRule()
		}
		rel -= 3
	}
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
		if e.lt != ref.lt {
			continue
		}
		base := e.start
		if e.sep {
			base += 3
		}
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
