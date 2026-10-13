package cli

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

// anchorBelow keeps ref's tail on its screen row across mutate.
func (t *transcript) anchorBelow(ref nodeRef, mutate func()) bool {
	t.buildIndex()
	before, ok := t.nodeSpanOf(ref)
	if !ok {
		mutate()
		return false
	}
	// A change entirely below the viewport must not move it.
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

func entryRowsStart(e *lineEntry) int { return e.start + e.sepHeight() }

// viewportSeedRef picks the node a cold selection starts on: the one in view, not
// the window's last.
func (t *transcript) viewportSeedRef(dir int) (nodeRef, bool) {
	top, bottom := t.viewportLines()
	var (
		startsIn, overlaps        nodeRef
		haveStartsIn, haveOverlap bool
	)
	take := func(ref nodeRef, first, last int) {
		if first >= top && first < bottom {
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
			break
		}
		// A ref's rows are contiguous within an entry.
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

// viewportLines reads the current geometry: the caller needs a fresh index.
func (t *transcript) viewportLines() (top, bottom int) {
	body, _ := t.layout(len(t.footLines()))
	return t.offset, t.offset + body
}
