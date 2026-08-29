package cli

// THE PICKER: one scrollable, selectable list, and the only one.
//
// The pager grew three of these independently. The completion menu had a
// sliding window with a "N–M of T" marker and ^N/^P cycling. The pit had a
// second window with a "… N more" marker and its own cursor. The help and
// status panels had neither, so a help panel taller than the pane simply lost
// its bottom — you could not scroll the list that tells you how to scroll.
//
// One component now, and every pit conforms to it: help, status, queue,
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
	rows []pitRow
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
	// dir is the direction of the last motion, and it exists because SOME TOPS
	// ARE NOT REPRESENTABLE. A marker never says "1 more", so a window cannot
	// sit one row from the top: showing that row means top = 0. Scrolling down
	// by one therefore has to round AWAY from where the reader came from --
	// without this, the first `j` in a long list snapped back to 0 and looked
	// like a dead key.
	dir int
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
func newPicker(rows []pitRow) *picker {
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
func (p *picker) setRows(rows []pitRow, keepID string) {
	prev, was := "", p.cursor
	if p.hasCursor() && p.cursor < len(p.rows) {
		prev = p.rows[p.cursor].id
	}
	if keepID == "" {
		keepID = prev
	}
	old := p.rows
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
	// THE ROW IS GONE: STAY WHERE YOU WERE STANDING. Falling back to the head
	// of the list is how Enter -- which CLOSES a value by removing the rows it
	// was made of -- threw a reader from the bottom of a 53-key form back to
	// the top, and how a message deleted from another shell put the cursor on
	// the one that runs next, with `x` under their finger.
	//
	// The fallback walks the OLD list outward from where the cursor was and
	// takes the first row that is still here -- backwards first, because the
	// row above is the one a collapsing value came out of and the one a reader
	// has already read. An index is not enough: the lists are different
	// lengths, and index 20 of the new list is a different key entirely.
	if p.cursor < 0 && was >= 0 {
		alive := make(map[string]int, len(rows))
		for i, r := range rows {
			if r.selectable() {
				if _, seen := alive[r.id]; !seen {
					alive[r.id] = i
				}
			}
		}
		for d := 1; p.cursor < 0 && d <= len(old); d++ {
			for _, j := range [2]int{was - d, was + d} {
				if j < 0 || j >= len(old) {
					continue
				}
				if i, ok := alive[old[j].id]; ok {
					p.cursor = i
					break
				}
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
	p.dir = sign(d)
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
	h := p.visible()
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
	p.dir = sign(d)
	p.top = clampInt(p.top+d, 0, p.maxTop())
}

// half is u/d: half a page, AND IT CHOOSES. Gluck: "u/d on the picker should
// move the cursor, not just the page." A half-page that left the selection
// behind meant the reader arrived somewhere with their cursor still upstairs,
// and the next ^N yanked the window back to it.
func (p *picker) half(d int) { p.pick(d * max(p.visible()/2, 1)) }

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
	h := p.visible()
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+h {
		p.top = p.cursor - h + 1
	}
	p.top = clampInt(p.top, 0, p.maxTop())
}

// visible is the list height as last drawn -- what a motion between paints
// must page by.
func (p *picker) visible() int {
	if p.height > 0 {
		return p.height
	}
	return min(pickerRows, max(len(p.rows), 1))
}

func (p *picker) maxTop() int { return max(len(p.rows)-p.visible(), 0) }

// selected is the row under the cursor, if any.
func (p *picker) selected() (pitRow, bool) {
	if !p.hasCursor() || p.cursor >= len(p.rows) {
		return pitRow{}, false
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
func (p *picker) lines(id pitID, w, h int) []string {
	budget := clampInt(h, 1, max(h, pickerRows))
	// THE WINDOW IS DECIDED HERE, TOP AND ALL. Every motion before this ran
	// against whatever height the picker was last DRAWN at -- and before the
	// first paint, against a guess -- so a motion could leave the cursor
	// outside the window it was about to get, and the list painted with no
	// highlight in it. That is what G, and k on the last row, did.
	markAbove, markBelow := p.window(budget)
	end := min(p.top+p.height, len(p.rows))

	// A LIST THAT OVERFLOWS KEEPS A CONSTANT HEIGHT, and it does so by
	// construction rather than by padding: window() sets the list height to
	// the budget MINUS the markers it chose, so rows plus markers is always
	// the budget. Where a marker is not needed, its row goes back to the list.
	// A list that FITS is drawn at its own size -- there is nothing to say
	// about it, and a blank row saying nothing is still a row.
	out := make([]string, 0, budget)
	if markAbove {
		out = append(out, pitGray(clipToWidth("  "+AndMore(p.top, "above"), w)))
	}
	mark := id.selectionGlyph()
	for i := p.top; i < end; i++ {
		prefix := "  "
		if i == p.cursor {
			prefix = mark + " "
		}
		// EVERY ROW THROUGH THE GATE (pitText, pit.go): rows come from forms,
		// command output and queued messages, and any of the three can carry
		// escape sequences. One place, so a new kind of pit cannot forget.
		row := clipToWidth(prefix+pitText(p.rows[i].text), w)
		if i == p.cursor {
			out = append(out, pitSelected(row))
			continue
		}
		out = append(out, row)
	}
	if markBelow {
		out = append(out, pitGray(clipToWidth("  "+AndMore(len(p.rows)-end, ""), w)))
	}
	return out[:min(len(out), budget)]
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// window decides the two marker rows, the list height and the top row inside a
// budget, and it obeys ONE RULE: A MARKER NEVER SAYS "1 MORE".
//
// Gluck: "obviously you shouldn't show 1 more, you should just show the 1 more
// there is. there is only ever a reason to put 2 more." A marker costs exactly
// the row it is describing, so "… 1 more" spends a row to say a row exists --
// the reader would rather have the row. At two or more it earns its place,
// because it stands in for several.
//
// (Checked against the field rather than invented. The terminal lists that get
// this right spend the row on content and put the position where it costs
// nothing: fzf draws a one-column scrollbar in the margin and keeps its counter
// in the info line; less puts a percentage in the status line. Nobody spends a
// whole row to hide a single line. The pit has no margin to draw in, so it
// keeps the marker for the many case and drops it for the one case.)
//
// THE ARITHMETIC, rather than a search over guesses. hiddenAbove is exactly
// top, hiddenBelow is exactly len-top-height, and each side must hide either
// NOTHING or TWO OR MORE. That pins a range of legal tops per configuration:
//
//	no marker above  → top = 0            marker above → top ≥ 2
//	no marker below  → top = len-height   marker below → top ≤ len-height-2
//
// So the choice is: which configuration can hold a top nearest the one the
// reader is already at, with the cursor still inside it. Fewer markers wins a
// tie, because a marker is a row of list the reader does not get.
func (p *picker) window(budget int) (above, below bool) {
	want := clampInt(p.top, 0, max(len(p.rows)-1, 0))
	best, bestCost := -1, 0
	var bestTop, bestHeight int
	for i, cfg := range [4][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		a, b := cfg[0], cfg[1]
		height := budget - boolToInt(a) - boolToInt(b)
		if height < 1 {
			continue
		}
		// bottom is the top that shows the last row: 0 when the whole list
		// fits, which is the case the first cut of this arithmetic got wrong
		// (len-height went negative and rejected every configuration, so a
		// list of eleven in a budget of twelve fell through to two markers
		// counting one row each -- the exact thing this function exists to
		// prevent).
		bottom := max(len(p.rows)-height, 0)
		lo, hi := 0, bottom
		if a {
			lo = max(lo, 2)
		} else {
			hi = min(hi, 0)
		}
		if b {
			hi = min(hi, len(p.rows)-height-2)
		} else {
			lo = max(lo, bottom)
		}
		if lo > hi {
			continue
		}
		top := clampInt(want, lo, hi)
		if p.hasCursor() {
			// The cursor must be inside the window this configuration paints.
			if p.cursor < top {
				top = clampInt(p.cursor, lo, hi)
			}
			if p.cursor >= top+height {
				top = clampInt(p.cursor-height+1, lo, hi)
			}
			if p.cursor < top || p.cursor >= top+height {
				continue
			}
		}
		cost := abs(top-want)*4 + boolToInt(a) + boolToInt(b)
		// Round AWAY from where the reader came from: a top that is not
		// representable (one row from an edge) must resolve in the direction
		// of travel, or the motion looks like a key that did nothing.
		if (p.dir > 0 && top < want) || (p.dir < 0 && top > want) {
			cost += 2
		}
		if best < 0 || cost < bestCost {
			best, bestCost, bestTop, bestHeight = i, cost, top, height
		}
	}
	if best < 0 {
		// Only reachable when the budget cannot hold the cursor at all; two
		// markers is the honest answer and follow() will place the window.
		p.height = max(budget-2, 1)
		return true, true
	}
	p.top, p.height = bestTop, bestHeight
	return best == 2 || best == 3, best == 1 || best == 3
}

// AndMore is THE spelling of a truncated list, and it lives here because the
// picker is where truncation happens. `figaro ls`, `form show` and the pit
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
