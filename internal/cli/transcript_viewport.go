package cli

// ---------------------------------------------------------------------------
// Viewport geometry over the line index.
//
// The index owns the line space; this owns the WINDOW onto it. The question
// here is what the viewport should do when content CHANGES HEIGHT underneath
// it — which half of the screen is allowed to move.
// ---------------------------------------------------------------------------

// shiftViewport moves the viewport by delta lines, clamped to the line space.
//
// The HIGH clamp is close to unreachable on an expansion — line space and
// maxOff both grow by exactly the number of rows added, so a viewport that was
// in range stays in range — and is kept because nothing here should be able to
// put t.offset out of bounds for the frame path to trip over.
//
// The LOW clamp is the reachable one: COLLAPSING moves the offset UP, and a
// change straddling the viewport top removes rows both above and below it while
// the delta counts all of them. The offset then asks to go above line 0, stops,
// and the anchor slides up the screen by the shortfall. That is the most that
// can be honoured.
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

// anchorBelow re-pins the viewport so that ref's tail — and therefore
// everything after it — keeps its screen row across a height change. mutate
// performs the change.
//
// THE TRANSCRIPT IS TEMPORAL: content earlier on screen was generated earlier.
// So when a block changes height it is the EARLIER portion of the screen that
// must move, upward and off the top, while everything at or below the change
// holds still — that is where the reader's eye and mental model are anchored.
//
// t.offset is an ABSOLUTE line index, so doing nothing pins the viewport TOP
// and grows the content DOWNWARD, shoving later content off the bottom. That
// is the wrong half. Shifting the offset by exactly the number of rows added
// above the anchor converts the same edit into upward growth.
//
// The anchor is the span's LAST line rather than the following line because
// the line after may not exist on both sides of the change (the block can be
// the last thing in the window); the delta is identical either way, and
// span.last is always addressable.
//
// Collapsing is the same arithmetic with a negative delta: the gap closes
// upward and the content below keeps its rows.
//
// Reports whether it anchored. It cannot when ref has no span on one side of
// the change — it was never in the index, or the change removed it — and the
// caller then keeps its previous behaviour rather than inventing a delta.
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

// viewportLines is the half-open line range the body is currently showing,
// [top, bottom). It reads the CURRENT geometry — an open panel and the
// follow-mode padding row both change the body height — so the caller must have
// a fresh index.
func (t *transcript) viewportLines() (top, bottom int) {
	body, _ := t.layout(len(t.footLines()))
	return t.offset, t.offset + body
}
