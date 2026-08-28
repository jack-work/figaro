package cli

// THE PICKER: one scrollable, selectable list, and the only one.
//
// The pager grew three of these independently. The completion menu had a
// sliding window with a "N–M of T" marker and ^N/^P cycling. The drawer had a
// second window with a "… N more" marker and its own cursor. The help and
// status panels had neither, so a help panel taller than the pane simply lost
// its bottom — you could not scroll the list that tells you how to scroll.
//
// One component now, and every drawer conforms to it: help, status, queue,
// notifications, command output, form show/listen, and the completion menu.
// What differs between them is the ROWS and the verb keys; what is shared is
// everything a reader's fingers touch.
//
//	j / k   ↓ / ↑     one row
//	^N / ^P           one row, and the SELECTION where there is one
//	u / d             half a page
//	gg / G  Home/End  the ends
//	Esc               close
//
// A picker with no selectable rows (help, status) still scrolls; a picker with
// them (queue, notifications, completions) also carries a cursor. That is the
// only difference, and it is a property of the ROWS rather than a mode.

import "fmt"

// picker is a window over rows, plus an optional cursor.
type picker struct {
	rows []drawerRow
	// cursor is the selected row, or -1 when this picker only scrolls. Only
	// selectable rows may hold it; the motions skip the chrome.
	cursor int
	// top is the first visible row. Pagination is a WINDOW over rows, never a
	// truncation of them, so the marker can be honest in both directions.
	top int
	// height is the last window height the picker was drawn at, so a motion
	// that happens between paints (a key, then a render) still pages by the
	// right amount.
	height int
}

// pickerRows is the tallest a picker may be, fixed rather than derived from the
// pane. Gluck: "Max page size should be fixed ideally". A list whose height
// moves with the terminal makes every position a different promise on a
// different screen.
const pickerRows = 12

// newPicker builds a list over rows. THERE IS NO "SELECTABLE" FLAG, and its
// absence is a bug fix: a picker used to be told at birth whether it had a
// cursor, and setRows inherited that answer from whatever the list looked like
// the first time it was built. The queue opened by `:send` was born holding one
// row -- "(none)", which is chrome -- so it was born cursorless and STAYED
// cursorless through every later refresh, and ^N did nothing until you closed
// and reopened the pit. Selectability is a property of the ROWS, asked afresh
// every time they change.
func newPicker(rows []drawerRow) *picker {
	p := &picker{cursor: -1}
	p.setRows(rows, "")
	return p
}

// setRows swaps the list in place, keeping everything the reader put there:
// the window, the height, and the SELECTION -- restored by row id rather than
// by index, because a queue that re-sorts under the cursor on every poll is a
// queue nobody can hit `x` on.
//
// The cursor is re-derived from the new rows: it appears when the list gains
// something selectable and leaves when the list loses it.
func (p *picker) setRows(rows []drawerRow, keepID string) {
	prev, hadCursor := "", p.hasCursor()
	if hadCursor && p.cursor < len(p.rows) {
		prev = p.rows[p.cursor].id
	}
	if keepID == "" {
		keepID = prev
	}
	p.rows = rows
	p.cursor = -1
	if keepID != "" {
		for i, r := range rows {
			if r.id == keepID && r.selectable() {
				p.cursor = i
				break
			}
		}
	}
	if p.cursor < 0 {
		p.cursor = p.nextSelectable(-1, 1)
	}
	p.top = clampInt(p.top, 0, p.maxTop())
	p.follow()
}

// hasCursor reports whether this list is one you CHOOSE in as well as read.
// It is the rows' answer, not the picker's history.
func (p *picker) hasCursor() bool { return p.cursor >= 0 }

// nextSelectable walks from i in direction d to the next row that can hold the
// cursor, or returns i unchanged when there is none.
func (p *picker) nextSelectable(i, d int) int {
	for n := i + d; n >= 0 && n < len(p.rows); n += d {
		if p.rows[n].selectable() {
			return n
		}
	}
	if i >= 0 && i < len(p.rows) && p.rows[i].selectable() {
		return i
	}
	return -1
}

// TWO MOTIONS, NOT ONE, and conflating them is what made `form show` unusable:
// j skipped whole properties, because it moved to the next SELECTABLE row and a
// form's child rows are not selectable. They are different questions.
//
//	j / k / arrows  →  step(±1): move the WINDOW by a row. Reading.
//	^N / ^P         →  pick(±1): move the CURSOR to the next item. Choosing.
//
// A list with no cursor answers both the same way, which is why the help panel
// feels identical; a list with one lets you read past the selection without
// dragging it along, which is what a long form needs.

// step scrolls by rows, and carries the cursor along only when the cursor
// would otherwise leave the window. Reading never changes what is selected.
func (p *picker) step(d int) {
	if len(p.rows) == 0 {
		return
	}
	p.scroll(d)
	if p.hasCursor() {
		p.dragCursorIntoView()
	}
}

// pick moves the SELECTION to the next selectable row, and slides the window to
// keep it visible.
func (p *picker) pick(d int) {
	if len(p.rows) == 0 {
		return
	}
	if !p.hasCursor() {
		p.scroll(d)
		return
	}
	for i := 0; i < abs(d); i++ {
		n := p.nextSelectable(p.cursor, sign(d))
		if n < 0 || n == p.cursor {
			break
		}
		p.cursor = n
	}
	p.follow()
}

// move is pick, kept for callers that mean "choose". Reading is step.
func (p *picker) move(d int) { p.pick(d) }

// dragCursorIntoView keeps the selection inside the window after a scroll, so
// a `y` or an `x` after paging always acts on something the reader can see.
func (p *picker) dragCursorIntoView() {
	h := p.window()
	if p.cursor < p.top {
		if n := p.nextSelectable(p.top-1, 1); n >= 0 && n < p.top+h {
			p.cursor = n
		}
		return
	}
	if p.cursor >= p.top+h {
		if n := p.nextSelectable(p.top+h, -1); n >= p.top {
			p.cursor = n
		}
	}
}

// scroll moves the window without touching a cursor.
func (p *picker) scroll(d int) {
	p.top = clampInt(p.top+d, 0, p.maxTop())
}

// half is u/d: half a window, and it READS -- a half-page is a scroll, not a
// selection.
func (p *picker) half(d int) { p.step(d * max(p.window()/2, 1)) }

// home / end are gg and G.
func (p *picker) home() {
	p.top = 0
	if p.hasCursor() {
		p.cursor = p.nextSelectable(-1, 1)
	}
}

func (p *picker) end() {
	p.top = p.maxTop()
	if p.hasCursor() {
		p.cursor = p.nextSelectable(len(p.rows), -1)
	}
}

// follow slides the window to keep the cursor in view, which is what makes
// ^N past the bottom edge scroll rather than lose the selection.
func (p *picker) follow() {
	h := p.window()
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+h {
		p.top = p.cursor - h + 1
	}
	p.top = clampInt(p.top, 0, p.maxTop())
}

func (p *picker) window() int {
	if p.height > 0 {
		return p.height
	}
	return min(pickerRows, max(len(p.rows), 1))
}

func (p *picker) maxTop() int { return max(len(p.rows)-p.window(), 0) }

// selected is the row under the cursor, if any.
func (p *picker) selected() (drawerRow, bool) {
	if !p.hasCursor() || p.cursor >= len(p.rows) {
		return drawerRow{}, false
	}
	return p.rows[p.cursor], true
}

// remove drops the selected row optimistically, leaving the cursor on what
// takes its place.
func (p *picker) remove() {
	if !p.hasCursor() || p.cursor >= len(p.rows) {
		return
	}
	p.rows = append(p.rows[:p.cursor], p.rows[p.cursor+1:]...)
	if n := p.nextSelectable(p.cursor-1, 1); n >= 0 {
		p.cursor = n
	} else {
		p.cursor = p.nextSelectable(len(p.rows), -1)
	}
	p.follow()
}

// lines draws the window: the visible rows, the selection marker, and an
// honest count of what is out of view on either side.
// lines draws EXACTLY h rows, always. The markers are part of the budget, not
// an addition to it: they appear and vanish as you scroll, and a pit whose
// height moved by a row every time you crossed the top of the list made the
// transcript above it jump by a row too. A window that changes size while you
// read it is the thing this component exists to stop.
func (p *picker) lines(id drawerID, w, h int) []string {
	budget := clampInt(h, 1, max(h, pickerRows))
	// Reserve the marker rows FIRST, out of the same budget.
	markAbove, markBelow := 0, 0
	if len(p.rows) > budget {
		// A list that does not fit shows at least one marker; which one
		// depends on where the window sits, so both are reserved and the
		// visible count is fixed either way.
		markAbove, markBelow = 1, 1
	}
	p.height = max(budget-markAbove-markBelow, 1)
	p.top = clampInt(p.top, 0, p.maxTop())
	end := min(p.top+p.window(), len(p.rows))

	out := make([]string, 0, budget)
	if markAbove > 0 {
		if p.top > 0 {
			out = append(out, drawerGray(clipToWidth("  "+AndMore(p.top, "above"), w)))
		} else {
			out = append(out, "")
		}
	}
	mark := id.selectionGlyph()
	for i := p.top; i < end; i++ {
		prefix := "  "
		if i == p.cursor {
			prefix = mark + " "
		}
		row := clipToWidth(prefix+p.rows[i].text, w)
		if i == p.cursor {
			out = append(out, drawerSelected(row))
			continue
		}
		out = append(out, row)
	}
	if markBelow > 0 {
		if rest := len(p.rows) - end; rest > 0 {
			out = append(out, drawerGray(clipToWidth("  "+AndMore(rest, ""), w)))
		} else {
			out = append(out, "")
		}
	}
	return out
}

// AndMore is THE spelling of a truncated list, and it lives here because the
// picker is where truncation happens. `figaro ls`, `form show` and the drawer
// had three spellings of it; a list that lies about its own length is the one
// failure mode all of them share.
func AndMore(n int, where string) string {
	if n <= 0 {
		return ""
	}
	if where == "" {
		return fmt.Sprintf("… %d more", n)
	}
	return fmt.Sprintf("… %d more %s", n, where)
}
