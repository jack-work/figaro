package cli

import (
	"strings"
	"unicode/utf8"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// start is the absolute line of the entry's first line: the separator's first
// blank when sep is set.
type lineEntry struct {
	turn int
	// key is the slice, not just the turn: a tall turn is several entries and
	// the viewport anchor must tell them apart.
	key   sliceKey
	start int
	sep   bool // preceded by the ""/rule separator pair
	open  bool // the live message, re-rendered every frame
	rows  []transcriptRow
	// A gap entry has no rows: it renders as exactly one row, synthesized per
	// frame (transcript.gapRow).
	gap *aria.Gap
}

func (e *lineEntry) isGap() bool { return e.gap != nil }

// A blank, then the rule, with the next message's voice header directly beneath.
const sepRows = 2

func (e *lineEntry) sepHeight() int {
	if e.sep {
		return sepRows
	}
	return 0
}

func (e *lineEntry) height() int {
	if e.isGap() {
		return e.sepHeight() + 1
	}
	return e.sepHeight() + len(e.rows)
}

func (e *lineEntry) refAt(rel int) nodeRef {
	rel -= e.sepHeight()
	if rel < 0 || e.isGap() || rel >= len(e.rows) {
		return nodeRef{}
	}
	return e.rows[rel].ref
}

// entries and scratch ping-pong so a rebuild compares shapes without allocating.
type lineIndex struct {
	entries []lineEntry
	scratch []lineEntry
	total   int
	// rev is the windowRev this index was built from: the one authority on
	// "the page set changed".
	rev uint64
	// An index whose stamp still holds does not need rebuilding.
	stamp indexStamp
	built bool
}

// indexShape is everything the index is a function of except the live message.
type indexShape struct {
	rev       uint64 // aria.Client.Revision: something was folded
	windowRev uint64 // the window's floor moved
	rowRev    uint64 // cached rows were dropped
	w, h      int
	from      aria.Anchor
	tailWant  int
	tailTuned bool
	// resetting: whether this frame re-derives the tail; a walk into history
	// does not.
	resetting bool
	openKey   sliceKey
}

// indexStamp is a shape plus the spinner of a live message.
type indexStamp struct {
	shape indexShape
	// animTick is the frame counter only while the open message animates;
	// every other tick leaves the index as it was.
	animTick int
}

// Materializes nothing: the stamp must be cheaper than what it guards.
func (t *transcript) indexStamp() indexStamp {
	st := indexStamp{shape: indexShape{
		rev:       t.client.Revision(),
		windowRev: t.windowRev,
		rowRev:    t.rowRev,
		w:         t.w,
		h:         t.h,
		from:      t.from,
		tailWant:  t.tailWant,
		tailTuned: t.tailTuned,
		resetting: t.follow && t.search == nil && t.jump == nil,
	}}
	if turn, from, ok := t.client.OpenKey(); ok {
		st.shape.openKey = sliceKeyOf(turn, from)
		if t.client.OpenAnimating() {
			st.animTick = t.tick
		}
	}
	return st
}

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

// Idempotent and cheap: the stamp decides whether nothing, the live entry, or the
// whole window is redone.
func (t *transcript) buildIndex() {
	st := t.indexStamp()
	if t.index.built && st == t.index.stamp {
		return
	}
	if t.index.built && st.shape == t.index.stamp.shape {
		// Only the spinner moved; the live entry is the last, so redrawing
		// it moves no other start.
		if t.refreshOpenEntry(t.openMessage()) {
			t.index.stamp = st
			return
		}
	}
	t.rebuildIndex(t.openMessage())
}

// Reports false when the last entry is not the open message: the full walk is needed.
func (t *transcript) refreshOpenEntry(open *aria.Message) bool {
	if open == nil || len(t.index.entries) == 0 {
		return false
	}
	e := &t.index.entries[len(t.index.entries)-1]
	if !e.open || e.key != keyOf(*open) {
		return false
	}
	was := e.height()
	e.rows = t.renderOpenMsg(*open).rows
	if now := e.height(); now != was {
		t.index.total += now - was
		t.rebuildLineLT()
	}
	return true
}

func (t *transcript) rebuildIndex(live *aria.Message) {
	// A search or jump walks below the floor; resetting the tail here would
	// raise it back and re-request the same page forever, with the keyboard
	// dead. The tail is re-derived when the walk ends.
	if t.follow && t.search == nil && t.jump == nil {
		t.resetToTail()
		// The raised floor decides which message is live.
		live = t.openMessage()
	}
	if t.cacheW != t.w { // width changed: cached rows are stale
		t.rowCache = map[sliceKey]cachedMessage{}
		t.stickyCache = map[sliceKey]stickyQuestion{}
		t.cacheW = t.w
	}
	entries, total := t.index.scratch[:0], 0
	// lineEntry.height is the one authority on height; a separator marks a
	// turn boundary, not an entry boundary, since a long turn is delivered in
	// slices.
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
	// The only gap-aware consumer: the window is walked as segments and the
	// holes between them; everything else is simply told less
	// (forEachMessage).
	t.client.ForEachSegment(t.from, windowEnd, func(m aria.Message) bool {
		rows, ok := t.rowCache[keyOf(m)]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[keyOf(m)] = rows
		}
		add(m.Turn, keyOf(m), rows.rows, false, nil)
		return true
	}, func(g aria.Gap) bool {
		// A gap's key is its first missing anchor, which no held message
		// can carry.
		hole := g
		add(0, gapKey(g), nil, false, &hole)
		return true
	})
	if live != nil {
		add(live.Turn, keyOf(*live), t.renderOpenMsg(*live).rows, true, nil)
	}
	// windowRev is the authority; the shape diff only catches what the page
	// set cannot see.
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
	// Stamped from the state the walk landed on: resetToTail may have moved
	// the floor.
	t.index.stamp, t.index.built = t.indexStamp(), true
	if changed {
		t.rebuildLineLT()
	}
}

// Separator rows carry the following message's slice.
func (t *transcript) rebuildLineLT() {
	if cap(t.lineKey) < t.index.total {
		t.lineKey = make([]sliceKey, t.index.total)
	}
	t.lineKey = t.lineKey[:t.index.total]
	for k := range t.index.entries {
		e := &t.index.entries[k]
		// Driven by height(): counting e.rows leaves a gap entry with a
		// lineKey hole.
		for i := e.start; i < e.start+e.height(); i++ {
			t.lineKey[i] = e.key
		}
	}
}

// A hole's key is its first missing anchor, packed like (Turn, From); it moves
// when the hole shrinks, so the viewport anchor follows a fill.
func gapKey(g aria.Gap) sliceKey {
	return sliceKey(int64(g.From.Turn)<<sliceKeyFromBits | int64(g.From.Node&(1<<sliceKeyFromBits-1)))
}

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

// Rows come out of rowCache already clipped and gutter-prefixed; an undecorated
// row costs a slice read.
func (t *transcript) window(a, b int, dst []string) []string {
	dst = dst[:0]
	hl := t.activeHighlight()
	sel := t.selectionSpan()
	vis := t.visualSpan()
	cur, curCol, hasCur := -1, 0, false
	if t.visual.active() {
		if line, ok := t.visualCursorLine(); ok {
			cur, curCol, hasCur = line, t.visual.cursor.col, true
		}
	}
	band := t.selectionBand(sel, a, b)
	notes := t.annotations(a, b)
	focusNote := t.focusedAnnotation(a, b)
	t.forEachWindowRow(a, b, func(e *lineEntry, rel int) {
		line := e.start + rel
		row := t.entryLine(e, rel, hl, sel)
		if !e.refAt(rel).valid() && band.covers(line) {
			// Chrome inside the band: a band with holes in it is not a
			// band.
			row = washRow(row, t.w, term.NodeWash(false))
		}
		row = t.washNotes(line, row, notes)
		row = t.underlineNotes(line, row, focusNote)
		row = t.washVisual(line, row, vis)
		if hasCur && line == cur {
			restore, washTo := "", 0
			if from, to, ok := vis.cols(line, t.w); ok && curCol >= from && curCol < to {
				restore, washTo = term.SelectWash(), to // the cursor sits inside the wash
			}
			row = cursorCell(row, curCol, restore, washTo)
		}
		dst = append(dst, row)
	})
	return dst
}

// The band covers the selected blocks, the chrome between them and the chrome
// closing it up to the fencing rule; it stops at the first row of a block that is
// not selected.
type lineBand struct{ lo, hi int }

func (b lineBand) covers(line int) bool { return line >= b.lo && line <= b.hi }

// A selection reaching past [a, b) is clamped to what is on screen.
func (t *transcript) selectionBand(sel selectionSpan, a, b int) lineBand {
	band := lineBand{lo: 1, hi: 0} // empty until a selected row is found
	if !sel.active {
		return band
	}
	t.forEachWindowRow(a, b, func(e *lineEntry, rel int) {
		if ref := e.refAt(rel); ref.valid() && sel.mark(ref).selected {
			if band.hi < band.lo {
				band.lo = e.start + rel
			}
			band.hi = e.start + rel
		}
	})
	if band.hi < band.lo {
		return band
	}
	band.lo = t.bandEdge(band.lo, -1)
	band.hi = t.bandEdge(band.hi, +1)
	return band
}

func (t *transcript) bandEdge(from, step int) int {
	edge := from
	for line := from + step; line >= 0 && line < t.index.total; line += step {
		k := t.index.entryAt(line)
		if k < 0 {
			break
		}
		e := &t.index.entries[k]
		if e.refAt(line - e.start).valid() {
			break
		}
		rule := isRuleRow(t.entryLine(e, line-e.start, "", selectionSpan{}))
		if rule && step < 0 {
			break
		}
		edge = line
		if rule {
			break
		}
	}
	return edge
}

// A rule may open on a corner (the adornment's snake closes into one), so the test
// is the first or second cell.
func isRuleRow(row string) bool {
	rest, _ := firstVisible(row)
	if strings.HasPrefix(rest, "─") {
		return true
	}
	_, n := utf8.DecodeRuneInString(rest)
	return n > 0 && strings.HasPrefix(rest[n:], "─")
}

// Applied at paint time: the region moves on every keystroke and the rows do not.
func (t *transcript) washVisual(line int, row string, vis visualSpan) string {
	from, to, ok := vis.cols(line, t.w)
	if !ok {
		return row
	}
	return washColumns(row, from, to)
}

func (t *transcript) rowRefs(a, b int, dst []nodeRef) []nodeRef {
	t.forEachWindowRow(a, b, func(e *lineEntry, rel int) {
		dst = append(dst, e.refAt(rel))
	})
	return dst
}

// Search walks line space one candidate at a time through this.
func (t *transcript) lineAt(i int) string {
	k := t.index.entryAt(i)
	if k < 0 {
		return ""
	}
	e := &t.index.entries[k]
	return t.entryLine(e, i-e.start, t.activeHighlight(), t.selectionSpan())
}

// Decoration happens at paint time, not into rowCache: the cue is a function of
// the selection, which changes far more often than the rows.
func (t *transcript) entryLine(e *lineEntry, rel int, hl string, sel selectionSpan) string {
	if e.sep {
		// The one place the separator's rows are named rather than counted.
		switch rel {
		case 0:
			return ""
		case 1:
			return t.transRule()
		}
	}
	rel -= e.sepHeight()
	if e.isGap() {
		// The separator pair above a gap is deliberate: the sentinel row
		// plays the voice header of the block that is missing.
		return t.gapRow(e.gap)
	}
	return t.rowLine(e.rows[rel], hl, sel)
}

// The right gutter is never painted over: the address stops one column short and
// the adornment marker is put back after it.
func (t *transcript) rowLine(r transcriptRow, hl string, sel selectionSpan) string {
	line := r.text
	if t.more() && (r.mark != "" || len(r.more) > 0) {
		line = ldrender.Annotate(line, r.more, r.mark, r.gutter, t.w)
	}
	// The snake's head follows the cursor, so it is resolved here and not
	// baked into the cache.
	if r.spine.Kind != ldrender.SpineNone {
		if c := spineCursor(r.ref, sel); c != 0 {
			line = ldrender.OverlayColumn(line, r.spine.Col, spineGlyph(r.spine, r.ref.meta, c))
		}
	}
	if r.ref.valid() {
		line = decorateNodeRow(line, sel.mark(r.ref), t.w)
	}
	if hl != "" {
		line = highlightMatches(line, hl)
	}
	return line
}

// A block's snake knows one cursor: its own.
func spineCursor(ref nodeRef, sel selectionSpan) int {
	if !sel.active {
		return 0
	}
	if sel.focus.turn != ref.turn || sel.focus.index != ref.index {
		return 0
	}
	return sel.focus.meta
}

// selectionSpan answers "is this ref selected" by comparison rather than a map
// over every retained node.
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
	block := blockOf(ref)
	p := selectionPoint{nodeRef: block}
	if pointLess(p, selectionPoint{nodeRef: blockOf(s.lo.nodeRef)}) ||
		pointLess(selectionPoint{nodeRef: blockOf(s.hi.nodeRef)}, p) {
		return selectionMark{}
	}
	active := block == blockOf(s.focus)
	if ref.meta > 0 || s.focus.meta > 0 {
		active = ref == s.focus
	}
	return selectionMark{selected: true, active: active}
}

// Duplicated LTs (a held-open message shadowing a committed one) widen the span.
func (t *transcript) nodeSpanOf(ref nodeRef) (nodeSpan, bool) {
	return t.spanWhere(ref.turn, func(r nodeRef) bool { return r == ref })
}

func (t *transcript) blockSpanOf(ref nodeRef) (nodeSpan, bool) {
	block := blockOf(ref)
	return t.spanWhere(ref.turn, func(r nodeRef) bool { return blockOf(r) == block })
}

func (t *transcript) spanWhere(turn int, match func(nodeRef) bool) (nodeSpan, bool) {
	span, found := nodeSpan{}, false
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.turn != turn {
			continue
		}
		base := e.start + e.sepHeight()
		for i := range e.rows {
			if !match(e.rows[i].ref) {
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
