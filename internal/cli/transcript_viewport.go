package cli

// ---------------------------------------------------------------------------
// Viewport geometry over the line index.

// shiftViewport moves the viewport by delta lines, clamped to the line space.
func (t *transcript) shiftViewport(delta int) {
	if delta == 0 {
		return
	}
	t.offset += delta
	if t.offset < 0 {
		t.offset = 0
	}
	if _, maxOff := t.layout(len(t.footLines())); t.offset > maxOff {
		t.offset = maxOff
	}
}

// anchorBelow re-pins the viewport so that ref's tail, and therefore
// everything after it: keeps its screen row across a height change. mutate
// performs the change.
func (t *transcript) anchorBelow(ref nodeRef, mutate func()) bool {
	t.buildIndex()
	before, ok := t.nodeSpanOf(ref)
	if !ok {
		mutate()
		return false
	}
	// A change ENTIRELY BELOW the viewport must not move it. The rule is that
	// content at or below the change holds still; when none of that content is
	// on screen there is nothing to hold, and shifting anyway would scroll the
	// reader away from a part of the transcript that did not change. Reachable:
	// select a tool, scroll up away from it, then press Enter.
	_, bottom := t.viewportLines()
	belowViewport := before.first >= bottom
	mutate()
	if belowViewport {
		return true
	}
	t.buildIndex()
	after, ok := t.nodeSpanOf(ref)
	if !ok {
		return false
	}
	t.shiftViewport(after.last - before.last)
	return true
}

// entryRowsStart is the absolute line of an entry's first CONTENT row: its
// start, plus whatever separator rows precede it. The entry answers for its own
// separator (sepHeight), so this cannot go stale if the separator changes size.
func entryRowsStart(e *lineEntry) int { return e.start + e.sepHeight() }

// viewportSeedRef picks the node a COLD selection should start on: the one the
// reader is looking at, not the one the retained window happens to end with.
func (t *transcript) viewportSeedRef(dir int) (nodeRef, bool) {
	top, bottom := t.viewportLines()
	var (
		startsIn, overlaps        nodeRef
		haveStartsIn, haveOverlap bool
	)
	take := func(ref nodeRef, first, last int) {
		if first >= top && first < bottom {
			// dir<0 keeps overwriting and ends on the last match; dir>0 takes
			// the first and holds it.
			if dir < 0 || !haveStartsIn {
				startsIn, haveStartsIn = ref, true
			}
		}
		if first < bottom && last >= top {
			if dir < 0 || !haveOverlap {
				overlaps, haveOverlap = ref, true
			}
		}
	}
	for k := range t.index.entries {
		e := &t.index.entries[k]
		base := entryRowsStart(e)
		if base >= bottom && haveStartsIn {
			break // past the viewport, and we already have an answer
		}
		// A ref's rows are contiguous within an entry (renderMsgBase emits one
		// run per node), so a run ends where the ref changes.
		runRef, runFirst := nodeRef{}, -1
		flush := func(end int) {
			if runFirst >= 0 && runRef.valid() {
				take(runRef, runFirst, end)
			}
		}
		for i := range e.rows {
			if ref := e.rows[i].ref; ref != runRef {
				flush(base + i - 1)
				runRef, runFirst = ref, base+i
			}
		}
		flush(base + len(e.rows) - 1)
	}
	if haveStartsIn {
		return startsIn, true
	}
	return overlaps, haveOverlap
}

// viewportLines is the half-open line range the body is currently showing,
// [top, bottom). It reads the CURRENT geometry, an open panel and the
// follow-mode padding row both change the body height: so the caller must have
// a fresh index.
func (t *transcript) viewportLines() (top, bottom int) {
	body, _ := t.layout(len(t.footLines()))
	return t.offset, t.offset + body
}
