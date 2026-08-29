package cli

// THE PICKER: one scrollable, selectable list, and the only one. Every pit
// conforms to it -- help, status, queue, command output, a form -- and what
// differs between them is the rows and the verb keys.
//
//	j / k  ↓ / ↑  ^N / ^P   one row, moving the selection where there is one
//	u / d                   half a page
//	gg / G  Home/End        the ends
//
// A list with no selectable rows still scrolls; a cursor is a property of the
// ROWS, not a mode. (The completion menu is still its own: see
// transcript.completionLines.)

import "fmt"

// picker is a window over rows, plus an optional cursor.
type picker struct {
	rows   []pitRow
	cursor int // the selected row, or -1 when this list only scrolls
	top    int // the first visible row; pagination is a window, never a truncation
	height int // the height last drawn at, so a motion between paints pages right
	// dir is the direction of the last motion. Some tops are not
	// representable -- a marker never says "1 more", so a window cannot sit
	// one row from an edge -- and a motion must round away from where it came
	// from, or the first `j` in a long list snaps back and looks dead.
	dir int
}

// pickerRows is the tallest a picker may be: fixed, so that a position means
// the same thing on every screen.
const pickerRows = 12

// newPicker builds a list over rows. There is no "selectable" flag: a cursor
// is the rows' answer, asked afresh every time they change. (Born from a flag,
// a queue opened while empty stayed cursorless for life.)
func newPicker(rows []pitRow) *picker {
	p := &picker{cursor: -1}
	p.setRows(rows, "")
	return p
}

// setRows swaps the list in place, keeping the window and the selection --
// restored by row id, not index, because a queue re-sorts under the cursor.
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
	// THE ROW IS GONE: STAY WHERE YOU WERE STANDING. Walk the OLD list outward
	// from the cursor and take the first row still here, backwards first. The
	// head of the list is the wrong answer -- Enter closing a value threw a
	// reader to the top of the form -- and an index is not enough, because the
	// two lists are different lengths.
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

// hasCursor reports whether this list is one you choose in as well as read.
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

// TWO MOTIONS: step moves the window (reading), pick moves the cursor
// (choosing). A list with no cursor answers both the same way.

// step scrolls by rows, carrying the cursor only when it would leave the
// window.
func (p *picker) step(d int) {
	if len(p.rows) == 0 {
		return
	}
	p.scroll(d)
	if p.hasCursor() {
		p.dragCursorIntoView()
	}
}

// pick moves the selection to the next selectable row.
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

// dragCursorIntoView keeps the selection inside the window after a scroll.
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

// half is u/d: half a page, and it moves the CURSOR -- a half-page that left
// the selection behind snapped back on the next ^N.
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

// follow slides the window to keep the cursor in view.
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

// visible is the list height as last drawn.
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

// remove drops the selected row, leaving the cursor on what takes its place.
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
// honest count of what is out of view. A list that overflows always draws
// exactly h rows, so the transcript above it never jumps.
func (p *picker) lines(id pitID, w, h int) []string {
	budget := clampInt(h, 1, max(h, pickerRows))
	// The window is decided here, top and all: motions run against the height
	// last DRAWN at, so only this can guarantee the cursor is inside it.
	markAbove, markBelow := p.window(budget)
	end := min(p.top+p.height, len(p.rows))

	// Rows plus markers is always the budget: where a marker is not needed its
	// row goes back to the list. A list that FITS is drawn at its own size.
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
		// Every row through the gate (pitText, pit.go), once, so that no pit
		// can forget.
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

// window picks the marker rows, the height and the top inside a budget, under
// ONE RULE: A MARKER NEVER SAYS "1 MORE". It costs exactly the row it hides,
// so at one the reader would rather have the row; at two it stands in for
// several. (fzf and less agree: spend the row on content, put the position
// somewhere that costs nothing. The pit has no margin, so it drops the marker
// instead.)
//
// Arithmetic, not search. hiddenAbove is top and hiddenBelow is
// len-top-height, and each side hides nothing or two, which pins the legal
// tops:
//
//	no marker above → top = 0           marker above → top ≥ 2
//	no marker below → top = len-height  marker below → top ≤ len-height-2
//
// Pick the configuration holding a top nearest where the reader already is,
// with the cursor inside it; fewer markers breaks ties.
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
		// bottom is the top that shows the last row -- 0 when the list fits,
		// which a negative len-height would otherwise reject.
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
		// Round away from where the reader came from, or an unrepresentable
		// top makes the motion look like a dead key.
		if (p.dir > 0 && top < want) || (p.dir < 0 && top > want) {
			cost += 2
		}
		if best < 0 || cost < bestCost {
			best, bestCost, bestTop, bestHeight = i, cost, top, height
		}
	}
	if best < 0 {
		// Only when the budget cannot hold the cursor at all.
		p.height = max(budget-2, 1)
		return true, true
	}
	p.top, p.height = bestTop, bestHeight
	return best == 2 || best == 3, best == 1 || best == 3
}

// AndMore is the one spelling of a truncated list.
func AndMore(n int, where string) string {
	if n <= 0 {
		return ""
	}
	if where == "" {
		return fmt.Sprintf("… %d more", n)
	}
	return fmt.Sprintf("… %d more %s", n, where)
}
