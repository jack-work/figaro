package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// NodeView renders one block to terminal rows. Each row must be a single
// physical line ≤ width (use clip). tick advances animations (spinners).
type NodeView interface {
	Render(n livedoc.Node, width, tick int) []string
}

// diffRange returns the first and last indices where old and next differ
// (-1,-1 if identical), so only the changed row span is repainted.
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

// Incipit renders an aria stream inline — no alternate screen. Closed messages
// are printed to native scrollback exactly once (Seal) and never touched again;
// only the open message is a live, redrawable region (Open). A resize repaints
// just the open message — the bounded, mutable part — so committed history is
// never reflowed. That is the structural fix for the resize/duplication class:
// the immutability boundary (a sealed message) is also the resize boundary.
//
// Not safe for concurrent use; the caller serializes Open/Seal/Tick/Resize.
type Incipit struct {
	term    Terminal
	view    NodeView
	Bookend func() []string          // sealed after an assistant message (the two-row status footer)
	Rule    func() string            // sealed after any other message (a plain full-width rule)
	Header  func(role string) string // printed above each message; "" suppresses

	tick int

	// Open-message live region:
	liveLT int
	role   string   // open message's role; selects Bookend (assistant) vs Rule
	live   []string // rows on screen for the open message
	vt     int      // rows of the live region scrolled above the viewport
	cur    int      // cursor row within the live region (0 = top)
}

// NewIncipit returns an inline renderer drawing to term via view.
func NewIncipit(term Terminal, view NodeView) *Incipit {
	return &Incipit{term: term, view: view}
}

// Seal finalizes a closed message. If it's the message currently live, its rows
// are already on screen — drop the cursor below them and release the region.
// Otherwise (a message we never streamed — catch-up) print its rows fresh,
// prefaced with a blank line (same leading-space rule the live region applies
// via compose).
func (i *Incipit) Seal(m aria.Message) {
	if m.LT == i.liveLT && i.liveLT != 0 {
		i.dropBelow()
		i.reset()
		return
	}
	rows := i.renderNodes(m.Nodes)
	var b strings.Builder
	b.WriteString("\r\n") // leading blank — every message is prefaced with a newline
	if h := i.header(m.Role); h != "" {
		b.WriteString(h)
		b.WriteString("\r\n")
		b.WriteString("\r\n")
	}
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	io.WriteString(i.term, b.String())
}

// Resume rebuilds the inline view after the transcript pager closes. The
// alt-screen restored whatever partial live region was on screen when the pager
// opened; this clears it, prints the given closed messages to scrollback in full
// (the bookend follows the assistant only), and — if a message is still
// streaming — starts a fresh live region. The cursor lands on a new line below
// everything, so input resumes after the content like `figaro show`.
func (i *Incipit) Resume(closed []aria.Message, openLT int, openRole string, open []livedoc.Node) {
	io.WriteString(i.term, "\x1b[J") // clear the restored partial region
	i.reset()
	for _, m := range closed {
		i.printMessage(m)
	}
	if openLT != 0 {
		i.Open(openLT, openRole, open)
	}
}

// printMessage writes a closed message's rows to scrollback (bookend after an
// assistant message), leaving the cursor on a fresh line below. Each message is
// prefaced with a blank line plus the role header (when configured) — the same
// leading rule Seal/compose apply.
func (i *Incipit) printMessage(m aria.Message) {
	body := i.renderNodes(m.Nodes)
	if len(body) == 0 {
		return
	}
	rows := []string{""}
	if h := i.header(m.Role); h != "" {
		rows = append(rows, h, "")
	}
	rows = append(rows, body...)
	if seal := i.seal(m.Role); len(seal) > 0 {
		w, _ := i.term.Size()
		rows = append(rows, "")
		for _, s := range seal {
			rows = append(rows, clip(s, w))
		}
	}
	io.WriteString(i.term, strings.Join(rows, "\r\n")+"\r\n")
}

// Open paints (or repaints) the open message's blocks as the live region.
func (i *Incipit) Open(lt int, role string, nodes []livedoc.Node) {
	if lt != i.liveLT {
		// A new open message without a prior Seal: release whatever was live.
		if i.liveLT != 0 {
			i.dropBelow()
		}
		i.reset()
		i.liveLT = lt
	}
	i.role = role
	i.paint(i.compose(nodes))
}

// Tick advances spinner animation and repaints the open message.
func (i *Incipit) Tick(nodes []livedoc.Node) {
	if i.liveLT == 0 {
		return
	}
	i.tick++
	i.paint(i.compose(nodes))
}

// Resize repaints the open message at the new width — clearing from the live
// region's top downward only, so scrollback above is untouched.
func (i *Incipit) Resize(nodes []livedoc.Node) {
	if i.liveLT == 0 {
		return
	}
	io.WriteString(i.term, "\x1b[J") // erase from the live-region top to end of screen
	i.live = nil
	i.vt, i.cur = 0, 0
	i.paint(i.compose(nodes))
}

func (i *Incipit) compose(nodes []livedoc.Node) []string {
	body := i.renderNodes(nodes)
	// Every message is prefaced with a blank row and (when configured) a role
	// header — sealed into scrollback alongside the rest of the live region.
	rows := make([]string, 0, len(body)+5)
	rows = append(rows, "")
	if h := i.header(i.role); h != "" {
		rows = append(rows, h, "")
	}
	rows = append(rows, body...)
	if seal := i.seal(i.role); len(seal) > 0 {
		w, _ := i.term.Size()
		rows = append(rows, "")
		for _, s := range seal {
			rows = append(rows, clip(s, w))
		}
	}
	return rows
}

// header returns the role-header line for role (e.g. "❯ you") or "" if no
// Header function is configured or the role has no glyph.
func (i *Incipit) header(role string) string {
	if i.Header == nil {
		return ""
	}
	return i.Header(role)
}

// seal returns the rows that close a message of the given role: the two-row
// status bookend after an assistant message, otherwise a plain full-width rule
// (so the user's prompt is still separated from the reply). Empty if neither
// is configured.
func (i *Incipit) seal(role string) []string {
	if role == "assistant" && i.Bookend != nil {
		return i.Bookend()
	}
	if i.Rule != nil {
		return []string{i.Rule()}
	}
	return nil
}

func (i *Incipit) renderNodes(nodes []livedoc.Node) []string {
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
func (i *Incipit) paint(newRows []string) {
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

func (i *Incipit) vmove(b *strings.Builder, target int) {
	if d := target - i.cur; d > 0 {
		fmt.Fprintf(b, "\x1b[%dB", d)
	} else if d < 0 {
		fmt.Fprintf(b, "\x1b[%dA", -d)
	}
	i.cur = target
}

// AbandonOpen ends the live region without a normal Seal (no figaro.aria
// close frame arrived). It moves the cursor past the live content and prints
// line on a fresh row as a visual boundary, so the next stream lands on clean
// ground. Use this when the agent dies mid-turn, the user disconnects with
// Ctrl-D, or an interrupt times out.
//
// line is typically a dim rule with a reason label — the caller owns the
// formatting (CLI policy). Without a live region, line is still printed.
func (i *Incipit) AbandonOpen(line string) {
	var b strings.Builder
	if i.liveLT != 0 {
		// dropBelow logic: park below the visible live span.
		if n := len(i.live) - i.vt; n > 0 {
			b.WriteString(strings.Repeat("\r\n", n))
		}
	}
	if line != "" {
		w, _ := i.term.Size()
		b.WriteString("\r\n")
		b.WriteString(clip(line, w))
		b.WriteString("\r\n")
	}
	io.WriteString(i.term, b.String())
	i.reset()
}

func (i *Incipit) dropBelow() {
	// The cursor is parked at the region's visible top (logical row i.vt). When
	// the region scrolled taller than the viewport, its first i.vt rows are above
	// the screen, so the visible span is len-i.vt — using len would leave i.vt
	// blank lines after the bookend.
	if n := len(i.live) - i.vt; n > 0 {
		io.WriteString(i.term, strings.Repeat("\r\n", n))
	}
}

func (i *Incipit) reset() {
	i.liveLT, i.role, i.live, i.vt, i.cur = 0, "", nil, 0, 0
}
