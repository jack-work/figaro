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

// Inline renders an aria stream inline — no alternate screen. Closed messages
// are printed to native scrollback exactly once (Seal) and never touched again.
// WITHIN the open message, finalized nodes are also committed to scrollback as
// soon as they stop mutating (flushStable): only the still-mutating tail is a
// live, redrawable region. That relocates the immutability boundary from the
// sealed message down to the node — so a turn taller than the viewport never
// keeps off-screen rows redrawable, which is the structural fix for the
// paint-can't-cross-the-viewport-top duplication. A resize repaints just the
// live tail; committed history (sealed messages AND flushed nodes) is never
// reflowed.
//
// Not safe for concurrent use; the caller serializes Open/Seal/Tick/Resize.
type Inline struct {
	term    Terminal
	view    NodeView
	Bookend func() string            // sealed after an assistant message (the id·time watermark)
	Rule    func() string            // sealed after any other message (a plain full-width rule)
	Header  func(role string) string // printed above each message; "" suppresses

	// StableForm renders nodes[from:to] in the immutable form committed to
	// scrollback (e.g. log-emitting tools collapse to a one-line indication).
	// LiveIndex reports the first still-mutating node; everything before it is
	// final and flushable. Both nil disables node-level flushing — the whole
	// open message stays one live region (pre-flush behavior; used by tests).
	StableForm func(nodes []livedoc.Node, from, to, width int) []string
	LiveIndex  func(nodes []livedoc.Node) int

	tick int

	// Open-message live region:
	liveLT       int
	role         string   // open message's role; selects Bookend (assistant) vs Rule
	live         []string // rows on screen for the live tail
	vt           int      // rows of the live region scrolled above the viewport
	cur          int      // cursor row within the live region (0 = top)
	flushedNodes int      // count of leading open-message nodes already committed to scrollback
	sawHeader    bool     // leading blank + role header already committed
}

// NewInline returns an inline renderer drawing to term via view.
func NewInline(term Terminal, view NodeView) *Inline {
	return &Inline{term: term, view: view}
}

// Seal finalizes a closed message. If it's the message currently live, its rows
// are already on screen — drop the cursor below them and release the region.
// Otherwise (a message we never streamed — catch-up) print its rows fresh,
// prefaced with a blank line (same leading-space rule the live region applies
// via compose).
func (i *Inline) Seal(m aria.Message) {
	if m.LT == i.liveLT && i.liveLT != 0 {
		// Repaint the final node state first: this flushes any node that only
		// reached its terminal form at seal (so a log-emitting tool collapses
		// to its done-indication in scrollback even if no Open carried the
		// finished status). Then park below the remaining live tail.
		i.Open(m.LT, m.Role, m.Nodes)
		i.dropBelow()
		i.reset()
		return
	}
	rows := i.scrollbackRows(m.Nodes)
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
func (i *Inline) Resume(closed []aria.Message, openLT int, openRole string, open []livedoc.Node) {
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
func (i *Inline) printMessage(m aria.Message) {
	body := i.scrollbackRows(m.Nodes)
	if len(body) == 0 {
		return
	}
	rows := []string{""}
	if h := i.header(m.Role); h != "" {
		rows = append(rows, h, "")
	}
	rows = append(rows, body...)
	if seal := i.seal(m.Role); seal != "" {
		w, _ := i.term.Size()
		rows = append(rows, "", clip(seal, w))
	}
	io.WriteString(i.term, strings.Join(rows, "\r\n")+"\r\n")
}

// Open paints (or repaints) the open message's blocks as the live region.
func (i *Inline) Open(lt int, role string, nodes []livedoc.Node) {
	if lt != i.liveLT {
		// A new open message without a prior Seal: release whatever was live.
		if i.liveLT != 0 {
			i.dropBelow()
		}
		i.reset()
		i.liveLT = lt
	}
	i.role = role
	i.flushStable(nodes)
	i.paint(i.compose(nodes))
}

// Tick advances spinner animation and repaints the open message.
func (i *Inline) Tick(nodes []livedoc.Node) {
	if i.liveLT == 0 {
		return
	}
	i.tick++
	i.flushStable(nodes)
	i.paint(i.compose(nodes))
}

// Resize repaints the open message at the new width — clearing from the live
// region's top downward only, so scrollback above is untouched.
func (i *Inline) Resize(nodes []livedoc.Node) {
	if i.liveLT == 0 {
		return
	}
	io.WriteString(i.term, "\x1b[J") // erase from the live-region top to end of screen
	i.live = nil
	i.vt, i.cur = 0, 0
	i.paint(i.compose(nodes))
}

func (i *Inline) compose(nodes []livedoc.Node) []string {
	start := i.flushedNodes
	if start > len(nodes) {
		start = len(nodes)
	}
	body := i.renderNodes(nodes[start:])
	// The live region is prefaced with a blank row and (until flushed) the role
	// header. Once flushStable has committed the header to scrollback (sawHeader)
	// only the separator blank remains — and even that is dropped when nothing
	// live is left but the bookend, so it doesn't double up above the seal line.
	rows := make([]string, 0, len(body)+5)
	if !i.sawHeader || len(body) > 0 {
		rows = append(rows, "")
	}
	if !i.sawHeader {
		if h := i.header(i.role); h != "" {
			rows = append(rows, h, "")
		}
	}
	rows = append(rows, body...)
	if seal := i.seal(i.role); seal != "" {
		w, _ := i.term.Size()
		rows = append(rows, "", clip(seal, w))
	}
	return rows
}

// flushStable commits the open message's newly-finalized leading nodes to
// native scrollback, so only the still-mutating tail stays redrawable. The
// cursor is parked at the live-region top (paint leaves it there), so the
// committed rows overwrite the identical live rows in place and scroll up as
// history; the remaining live rows below are cleared and repainted fresh by the
// following paint. This is what keeps the live region bounded — off-screen rows
// are never addressed again, so paint's cursor math never has to cross the
// viewport top.
func (i *Inline) flushStable(nodes []livedoc.Node) {
	if i.StableForm == nil || i.LiveIndex == nil {
		return
	}
	firstLive := i.LiveIndex(nodes)
	if firstLive <= i.flushedNodes {
		return
	}
	w, _ := i.term.Size()
	if w <= 0 {
		w = 80
	}
	var out []string
	out = append(out, "") // leading blank: header separator (first) or block separator
	if !i.sawHeader {
		if h := i.header(i.role); h != "" {
			out = append(out, h, "")
		}
		i.sawHeader = true
	}
	out = append(out, i.StableForm(nodes, i.flushedNodes, firstLive, w)...)

	var b strings.Builder
	for _, r := range out {
		b.WriteString("\x1b[2K")
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	b.WriteString("\x1b[J") // clear the stale live rows below the new region top
	io.WriteString(i.term, b.String())

	i.flushedNodes = firstLive
	i.live = nil
	i.vt, i.cur = 0, 0
}

// scrollbackRows renders a whole closed message in its scrollback form (log-
// emitting tools collapsed) when a StableForm is configured, else the plain
// live render. Used by the paths that print a message straight to scrollback
// without ever streaming it (catch-up Seal, Resume, printMessage).
func (i *Inline) scrollbackRows(nodes []livedoc.Node) []string {
	if i.StableForm != nil {
		w, _ := i.term.Size()
		if w <= 0 {
			w = 80
		}
		return i.StableForm(nodes, 0, len(nodes), w)
	}
	return i.renderNodes(nodes)
}

// header returns the role-header line for role (e.g. "❯ you") or "" if no
// Header function is configured or the role has no glyph.
func (i *Inline) header(role string) string {
	if i.Header == nil {
		return ""
	}
	return i.Header(role)
}

// seal returns the line that closes a message of the given role: the id·time
// bookend after an assistant message, otherwise a plain full-width rule (so the
// user's prompt is still separated from the reply). "" if neither is configured.
func (i *Inline) seal(role string) string {
	if role == "assistant" && i.Bookend != nil {
		return i.Bookend()
	}
	if i.Rule != nil {
		return i.Rule()
	}
	return ""
}

func (i *Inline) renderNodes(nodes []livedoc.Node) []string {
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
	// A fresh region (nothing on screen yet, e.g. right after a flush re-homed
	// the cursor to a new region top) can't be diff-painted: diffRange would
	// skip a leading blank row that coincidentally matches the empty old state,
	// and the cursor-down used to reach the first real row clamps at the
	// viewport bottom when the region top sits there. Print it from scratch,
	// growing downward with \r\n (which scrolls) instead.
	if len(i.live) == 0 {
		i.printFresh(newRows)
		return
	}
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

// printFresh draws newRows from the current cursor (assumed parked at the region
// top) growing downward with \r\n so the terminal scrolls naturally when the
// region reaches the viewport bottom — never a cursor-down, which would clamp.
// It then parks the cursor at the top of the visible region, matching paint's
// exit contract.
func (i *Inline) printFresh(newRows []string) {
	_, h := i.term.Size()
	var b strings.Builder
	b.WriteString("\x1b[?2026h") // synchronized output
	b.WriteString("\r")
	for k, r := range newRows {
		if k > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString("\x1b[2K")
		b.WriteString(r)
	}
	i.cur = len(newRows) - 1
	if i.cur < 0 {
		i.cur = 0
	}
	// Rows past the viewport bottom scrolled above its top; the visible region
	// begins at logical row vt.
	if h > 0 && i.cur > h-1 {
		i.vt = i.cur - (h - 1)
	} else {
		i.vt = 0
	}
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

// AbandonOpen ends the live region without a normal Seal (no figaro.aria
// close frame arrived). It moves the cursor past the live content and prints
// line on a fresh row as a visual boundary, so the next stream lands on clean
// ground. Use this when the agent dies mid-turn, the user disconnects with
// Ctrl-D, or an interrupt times out.
//
// line is typically a dim rule with a reason label — the caller owns the
// formatting (CLI policy). Without a live region, line is still printed.
func (i *Inline) AbandonOpen(line string) {
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

func (i *Inline) dropBelow() {
	// The cursor is parked at the region's visible top (logical row i.vt). When
	// the region scrolled taller than the viewport, its first i.vt rows are above
	// the screen, so the visible span is len-i.vt — using len would leave i.vt
	// blank lines after the bookend.
	if n := len(i.live) - i.vt; n > 0 {
		io.WriteString(i.term, strings.Repeat("\r\n", n))
	}
}

func (i *Inline) reset() {
	i.liveLT, i.role, i.live, i.vt, i.cur = 0, "", nil, 0, 0
	i.flushedNodes, i.sawHeader = 0, false
}
