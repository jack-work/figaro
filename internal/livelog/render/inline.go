package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// NodeView renders one aria block to terminal rows. Each row must be a single
// physical line ≤ width (use clip). tick advances animations (spinners).
type NodeView interface {
	Render(n aria.Node, width, tick int) []string
}

// Inline renders an aria stream inline — no alternate screen. Closed messages
// are printed to native scrollback exactly once (Seal) and never touched again;
// only the open message is a live, redrawable region (Open). A resize repaints
// just the open message — the bounded, mutable part — so committed history is
// never reflowed. That is the structural fix for the resize/duplication class:
// the immutability boundary (a sealed message) is also the resize boundary.
//
// Not safe for concurrent use; the caller serializes Open/Seal/Tick/Resize.
type Inline struct {
	term    Terminal
	view    NodeView
	Bookend func() string

	tick int

	// Open-message live region:
	liveLT int
	live   []string // rows on screen for the open message
	vt     int      // rows of the live region scrolled above the viewport
	cur    int      // cursor row within the live region (0 = top)
}

// NewInline returns an inline renderer drawing to term via view.
func NewInline(term Terminal, view NodeView) *Inline {
	return &Inline{term: term, view: view}
}

// Seal finalizes a closed message. If it's the message currently live, its rows
// are already on screen — drop the cursor below them and release the region.
// Otherwise (a message we never streamed — catch-up) print its rows fresh.
func (i *Inline) Seal(m aria.Message) {
	if m.LT == i.liveLT && i.liveLT != 0 {
		i.dropBelow()
		i.reset()
		return
	}
	rows := i.renderNodes(m.Nodes)
	var b strings.Builder
	for k, r := range rows {
		if k > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(r)
	}
	b.WriteString("\r\n")
	io.WriteString(i.term, b.String())
}

// Open paints (or repaints) the open message's blocks as the live region.
func (i *Inline) Open(lt int, role string, nodes []aria.Node) {
	if lt != i.liveLT {
		// A new open message without a prior Seal: release whatever was live.
		if i.liveLT != 0 {
			i.dropBelow()
		}
		i.reset()
		i.liveLT = lt
	}
	i.paint(i.compose(nodes))
}

// Tick advances spinner animation and repaints the open message.
func (i *Inline) Tick(nodes []aria.Node) {
	if i.liveLT == 0 {
		return
	}
	i.tick++
	i.paint(i.compose(nodes))
}

// Resize repaints the open message at the new width — clearing from the live
// region's top downward only, so scrollback above is untouched.
func (i *Inline) Resize(nodes []aria.Node) {
	if i.liveLT == 0 {
		return
	}
	io.WriteString(i.term, "\x1b[J") // erase from the live-region top to end of screen
	i.live = nil
	i.vt, i.cur = 0, 0
	i.paint(i.compose(nodes))
}

func (i *Inline) compose(nodes []aria.Node) []string {
	rows := i.renderNodes(nodes)
	if i.Bookend != nil {
		w, _ := i.term.Size()
		rows = append(rows, "", clip(i.Bookend(), w))
	}
	return rows
}

func (i *Inline) renderNodes(nodes []aria.Node) []string {
	w, _ := i.term.Size()
	if w <= 0 {
		w = 80
	}
	var rows []string
	for k, n := range nodes {
		if k > 0 {
			rows = append(rows, "")
		}
		for _, l := range i.view.Render(n, w, i.tick) {
			rows = append(rows, clip(l, w))
		}
	}
	return rows
}

// paint line-diffs newRows against the on-screen live region. Cursor enters and
// leaves at the top of the region.
func (i *Inline) paint(newRows []string) {
	first, last := diffRange(i.live, newRows)
	if first < 0 {
		return
	}
	_, h := i.term.Size()
	var b strings.Builder
	b.WriteString("\x1b[?2026h") // synchronized output
	// move to `first` (scroll if it's below the viewport bottom)
	if h > 0 {
		if bottom := i.vt + h - 1; first > bottom {
			i.vmove(&b, bottom)
			b.WriteString(strings.Repeat("\r\n", first-bottom))
			i.vt += first - bottom
			i.cur = first
		} else {
			i.vmove(&b, first)
		}
	} else {
		i.vmove(&b, first)
	}
	b.WriteString("\r")
	for k := first; k <= last; k++ {
		if k > first {
			b.WriteString("\r\n")
			i.cur++
			if h > 0 && i.cur > i.vt+h-1 {
				i.vt = i.cur - (h - 1)
			}
		}
		b.WriteString("\x1b[2K")
		if k < len(newRows) {
			b.WriteString(newRows[k])
		}
	}
	i.cur = last
	// park back at the top of the region
	i.vmove(&b, i.vt)
	b.WriteString("\r")
	b.WriteString("\x1b[?2026l")
	io.WriteString(i.term, b.String())
	i.live = newRows
}

func (i *Inline) vmove(b *strings.Builder, target int) {
	if d := target - i.cur; d > 0 {
		fmt.Fprintf(b, "\x1b[%dB", d)
	} else if d < 0 {
		fmt.Fprintf(b, "\x1b[%dA", -d)
	}
	i.cur = target
}

func (i *Inline) dropBelow() {
	if n := len(i.live); n > 0 {
		// cursor is at the region top; move to just past the last row.
		io.WriteString(i.term, strings.Repeat("\r\n", n))
	}
}

func (i *Inline) reset() {
	i.liveLT, i.live, i.vt, i.cur = 0, nil, 0, 0
}
