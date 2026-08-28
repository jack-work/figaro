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

func newPicker(rows []drawerRow, selectable bool) *picker {
	p := &picker{rows: rows, cursor: -1}
	if selectable {
		p.cursor = p.nextSelectable(-1, 1)
	}
	return p
}

func (p *picker) selectable() bool { return p.cursor >= 0 }

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

// move is the motion every drawer shares. It moves the CURSOR when there is
// one and the WINDOW when there is not, which is what lets `j` mean the same
// thing to a reader in the help panel and in the queue.
func (p *picker) move(d int) {
	if len(p.rows) == 0 {
		return
	}
	if p.selectable() {
		if n := p.nextSelectable(p.cursor, sign(d)); n >= 0 {
			for i := 0; i < abs(d) && n >= 0; i++ {
				p.cursor = n
				n = p.nextSelectable(p.cursor, sign(d))
			}
		}
		p.follow()
		return
	}
	p.scroll(d)
}

// scroll moves the window without touching a cursor.
func (p *picker) scroll(d int) {
	p.top = clampInt(p.top+d, 0, p.maxTop())
}

// half is u/d: half a window.
func (p *picker) half(d int) { p.move(d * max(p.window()/2, 1)) }

// home / end are gg and G.
func (p *picker) home() {
	p.top = 0
	if p.selectable() {
		p.cursor = p.nextSelectable(-1, 1)
	}
}

func (p *picker) end() {
	p.top = p.maxTop()
	if p.selectable() {
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
	if !p.selectable() || p.cursor >= len(p.rows) {
		return drawerRow{}, false
	}
	return p.rows[p.cursor], true
}

// remove drops the selected row optimistically, leaving the cursor on what
// takes its place.
func (p *picker) remove() {
	if !p.selectable() || p.cursor >= len(p.rows) {
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
func (p *picker) lines(id drawerID, w, h int) []string {
	p.height = clampInt(h, 1, pickerRows)
	p.top = clampInt(p.top, 0, p.maxTop())
	end := min(p.top+p.window(), len(p.rows))

	out := make([]string, 0, p.window()+2)
	if p.top > 0 {
		out = append(out, drawerGray(clipToWidth("  "+AndMore(p.top, "above"), w)))
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
	if rest := len(p.rows) - end; rest > 0 {
		out = append(out, drawerGray(clipToWidth("  "+AndMore(rest, ""), w)))
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
