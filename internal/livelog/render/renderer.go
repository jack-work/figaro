package render

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/livelog/doc"
)

// Renderer draws a list of blocks to a Terminal with pi-style differential
// rendering. It holds the whole frame (prev), and on each update line-diffs
// against it and rewrites only the changed span. Because it owns the screen, a
// resize — or any change to a line that has scrolled above the viewport — is
// handled by a clean full redraw rather than by trying (and failing) to reflow
// content that's no longer addressable. That is precisely the failure mode that
// duplicated content under a mid-stream terminal resize.
//
// Not safe for concurrent use; the caller serializes Render/Tick.
type Renderer struct {
	term Terminal
	view BlockRenderer

	// Bookend, if set, is a status line pinned just below the content (re-rendered
	// every frame, e.g. an aria id + clock). It is part of the frame.
	Bookend func() string

	blocks []doc.Block
	tick   int

	prev    []string // the frame currently on screen
	vt      int      // viewport top: lines that have scrolled above the viewport
	cur     int      // cursor row (absolute frame index)
	w, h    int      // last-rendered terminal size
	started bool
}

// New returns a Renderer drawing to term using view. If view is nil a
// TextRenderer is used.
func New(term Terminal, view BlockRenderer) *Renderer {
	if view == nil {
		view = TextRenderer{}
	}
	return &Renderer{term: term, view: view}
}

// Render draws blocks, reconciling the screen with the minimum writes.
func (r *Renderer) Render(blocks []doc.Block) {
	r.blocks = blocks
	r.redraw()
}

// Tick advances animations (spinners) and repaints; cheap when nothing visible
// changed (the diff finds no work).
func (r *Renderer) Tick() {
	r.tick++
	r.redraw()
}

func (r *Renderer) redraw() {
	w, h := r.term.Size()
	if w <= 0 {
		w = 80
	}
	next := r.compose(w)
	if !r.started || w != r.w || h != r.h {
		r.full(next, w, h)
		return
	}
	first, last := diffRange(r.prev, next)
	if first < 0 {
		return // nothing changed
	}
	if first < r.vt {
		// Change above the viewport (scrolled off): unreachable by relative moves.
		// We own the screen, so repaint it whole — no duplication, no stranding.
		r.full(next, w, h)
		return
	}
	r.patch(next, first, last)
}

// compose flattens the blocks (plus an optional bookend) into one line per row,
// a blank line separating adjacent blocks.
func (r *Renderer) compose(w int) []string {
	var lines []string
	for i, b := range r.blocks {
		if i > 0 {
			lines = append(lines, "")
		}
		for _, l := range r.view.Render(b, w, r.tick) {
			lines = append(lines, clip(l, w))
		}
	}
	if r.Bookend != nil {
		lines = append(lines, "", clip(r.Bookend(), w))
	}
	return lines
}

// full clears the screen + scrollback and repaints everything. Used on first
// draw, on resize, and to recover when an off-screen line changed.
func (r *Renderer) full(next []string, w, h int) {
	r.w, r.h, r.started = w, h, true
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[3J\x1b[H") // clear screen, clear scrollback, home
	for i, ln := range next {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(ln)
	}
	r.term.Write([]byte(b.String()))
	r.prev = next
	r.cur = max(0, len(next)-1)
	r.vt = 0
	if h > 0 && len(next) > h {
		r.vt = len(next) - h
	}
}

// patch rewrites only rows [first, last], scrolling as needed.
func (r *Renderer) patch(next []string, first, last int) {
	var b strings.Builder
	r.moveTo(&b, first)
	for i := first; i <= last; i++ {
		if i > first {
			b.WriteString("\r\n") // advance one row (scrolls at the viewport bottom)
			r.cur++
			if r.h > 0 && r.cur > r.vt+r.h-1 {
				r.vt = r.cur - (r.h - 1)
			}
		}
		b.WriteString("\x1b[2K") // erase the whole line
		if i < len(next) {
			b.WriteString(next[i])
		}
	}
	r.term.Write([]byte(b.String()))
	r.prev = next
	r.cur = last
}

// moveTo positions the cursor at row target (column 0), scrolling the viewport
// when target is below its bottom (newly appended rows push content up).
func (r *Renderer) moveTo(b *strings.Builder, target int) {
	if r.h > 0 {
		if bottom := r.vt + r.h - 1; target > bottom {
			r.vmove(b, bottom)
			b.WriteString(strings.Repeat("\r\n", target-bottom))
			r.vt += target - bottom
			r.cur = target
			b.WriteString("\r")
			return
		}
	}
	r.vmove(b, target)
	b.WriteString("\r")
	r.cur = target
}

// vmove emits the relative cursor move from r.cur to target (no scroll) and
// records it.
func (r *Renderer) vmove(b *strings.Builder, target int) {
	if d := target - r.cur; d > 0 {
		fmt.Fprintf(b, "\x1b[%dB", d)
	} else if d < 0 {
		fmt.Fprintf(b, "\x1b[%dA", -d)
	}
	r.cur = target
}

// diffRange returns the first and last row indices that differ between old and
// new (-1,-1 when identical). Appended rows and a shrink (old longer) both
// surface as a trailing changed range, so the patcher writes new tails and
// clears surplus old rows.
func diffRange(old, next []string) (first, last int) {
	first, last = -1, -1
	n := len(next)
	if len(old) > n {
		n = len(old)
	}
	for i := 0; i < n; i++ {
		var o, x string
		if i < len(old) {
			o = old[i]
		}
		if i < len(next) {
			x = next[i]
		}
		if o != x {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	return
}
