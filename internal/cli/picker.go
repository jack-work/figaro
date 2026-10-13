package cli

// Every pit is a picker; a cursor is a property of the rows, not a mode.

import "fmt"

type picker struct {
	rows   []pitRow
	cursor int // the selected row, or -1 when this list only scrolls
	top    int
	height int // the height last drawn at: a motion between paints pages against it
	// dir: a marker never says "1 more", so some tops are unrepresentable and
	// a motion must round away from where it came.
	dir int
}

// Fixed, so a position means the same thing on every screen.
const pickerRows = 12

// A cursor is the rows' answer, asked afresh every time they change; a flag went stale.
func newPicker(rows []pitRow) *picker {
	p := &picker{cursor: -1}
	p.setRows(rows, "")
	return p
}

// The selection is restored by row id, not index: a queue re-sorts under the cursor.
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
	// The row is gone: walk the old list outward from the cursor and take the
	// first row still here.
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

func (p *picker) focus(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range p.rows {
		if r.id == id && r.selectable() {
			p.cursor = i
			p.follow()
			return true
		}
	}
	return false
}

func (p *picker) find(q string, dir int) bool {
	if len(p.rows) == 0 {
		return false
	}
	if dir == 0 {
		dir = 1
	}
	from := p.cursor
	if from < 0 {
		from = p.top
	}
	for i := 1; i <= len(p.rows); i++ {
		n := ((from+dir*i)%len(p.rows) + len(p.rows)) % len(p.rows)
		if !p.rows[n].selectable() {
			continue
		}
		// The note is drawn, so it is searched.
		if searchContains(p.rows[n].text, q) || searchContains(p.rows[n].note, q) {
			p.cursor = n
			p.follow()
			return true
		}
	}
	return false
}

func (p *picker) hasCursor() bool { return p.cursor >= 0 }

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

// step moves the window (reading), pick moves the cursor (choosing).

func (p *picker) step(d int) {
	if len(p.rows) == 0 {
		return
	}
	p.scroll(d)
	if p.hasCursor() {
		p.dragCursorIntoView()
	}
}

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

// cycle wraps (a completion menu); pick stops at the ends (a list being read).
func (p *picker) cycle(d int) {
	n := len(p.rows)
	if n == 0 || d == 0 {
		return
	}
	p.dir = sign(d)
	first, last := p.nextSelectable(-1, 1), p.nextSelectable(n, -1)
	switch {
	case p.cursor < 0 && d > 0:
		p.cursor = first
	case p.cursor < 0:
		p.cursor = last
	default:
		next := p.nextSelectable(p.cursor, sign(d))
		if next < 0 || next == p.cursor {
			next = first
			if d < 0 {
				next = last
			}
		}
		p.cursor = next
	}
	p.follow()
}

func (p *picker) unselect() { p.cursor = -1 }

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

func (p *picker) scroll(d int) {
	p.dir = sign(d)
	p.top = clampInt(p.top+d, 0, p.maxTop())
}

// Moves the cursor, or the next ^N snaps back.
func (p *picker) half(d int) { p.pick(d * max(p.visible()/2, 1)) }

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

func (p *picker) visible() int {
	if p.height > 0 {
		return p.height
	}
	return min(pickerRows, max(len(p.rows), 1))
}

func (p *picker) maxTop() int { return max(len(p.rows)-p.visible(), 0) }

func (p *picker) selected() (pitRow, bool) {
	if !p.hasCursor() || p.cursor >= len(p.rows) {
		return pitRow{}, false
	}
	return p.rows[p.cursor], true
}

func (p *picker) neighbour() (pitRow, bool) {
	if !p.hasCursor() {
		return pitRow{}, false
	}
	for _, d := range [2]int{1, -1} {
		if n := p.nextSelectable(p.cursor, d); n >= 0 && n != p.cursor {
			return p.rows[n], true
		}
	}
	return pitRow{}, false
}

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

// A list that overflows always draws exactly h rows, so the transcript above it
// never jumps.
func (p *picker) lines(id pitID, w, h int) []string {
	budget := clampInt(h, 1, max(h, pickerRows))
	// Motions run against the height last drawn at, so only the draw can
	// guarantee the cursor is inside the window.
	markAbove, markBelow := p.window(budget)
	end := min(p.top+p.height, len(p.rows))

	// Rows plus markers is always the budget.
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
		// Every row passes pitText once.
		text := prefix + pitText(p.rows[i].text)
		if note := p.rows[i].note; note != "" {
			if i == p.cursor {
				text += "  " + pitText(note)
			} else {
				text += "  " + pitGray(pitText(note))
			}
		}
		row := clipToWidth(text, w)
		if i == p.cursor {
			out = append(out, pitSelected(row, p.rows[i].tone))
			continue
		}
		out = append(out, p.rows[i].tone.paint(row))
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

// window picks markers, height and top inside a budget. A marker never says "1
// more", so each side hides nothing or at least two: no marker above means top =
// 0, a marker above means top >= 2; no marker below means top = len-height, a
// marker below means top <= len-height-2. Nearest top to where the reader is,
// cursor inside, fewer markers on ties.
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
		// bottom is 0 when the list fits.
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
		// Round away from where the reader came from, or the motion looks
		// like a dead key.
		if (p.dir > 0 && top < want) || (p.dir < 0 && top > want) {
			cost += 2
		}
		if best < 0 || cost < bestCost {
			best, bestCost, bestTop, bestHeight = i, cost, top, height
		}
	}
	if best < 0 {
		p.height = max(budget-2, 1)
		return true, true
	}
	p.top, p.height = bestTop, bestHeight
	return best == 2 || best == 3, best == 1 || best == 3
}

func AndMore(n int, where string) string {
	if n <= 0 {
		return ""
	}
	if where == "" {
		return fmt.Sprintf("… %d more", n)
	}
	return fmt.Sprintf("… %d more %s", n, where)
}
