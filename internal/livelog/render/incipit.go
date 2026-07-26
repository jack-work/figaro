package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/render"
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
// are printed to native scrollback exactly once (Freeze) and never touched again;
// only the open message is a live, redrawable region (Open). A resize repaints
// just the open message — the bounded, mutable part — so committed history is
// never reflowed. That is the structural fix for the resize/duplication class:
// the immutability boundary (a frozen message) is also the resize boundary.
//
// Not safe for concurrent use; the caller serializes Open/Freeze/Tick/Resize.
type Incipit struct {
	term    Terminal
	view    NodeView
	Bookend func() []string          // closes an assistant message (the two-row status footer)
	Rule    func() string            // closes any other message (a plain full-width rule)
	Header  func(role string) string // printed above each message; "" suppresses

	tick     int
	thinking bool // open region is an OpenThinking placeholder (adopted by the next Open)

	// Open-message live region:
	liveTurn    int
	liveFrom    uint64   // start of the open suffix
	liveCount   int      // node count of the open suffix; (turn,from,count) is the region's identity
	liveInquiry string   // the turn's opening question, drawn above the nodes
	role        string   // open message's role; selects Bookend (assistant) vs Rule
	live        []string // rows on screen for the open message
	vt          int      // rows of the live region scrolled above the viewport
	cur         int      // cursor row within the live region (0 = top)
}

// NewIncipit returns an inline renderer drawing to term via view.
func NewIncipit(term Terminal, view NodeView) *Incipit {
	return &Incipit{term: term, view: view}
}

// Freeze finalizes a closed message. If it's the message currently live, its
// rows are already on screen — drop the cursor below them and release the
// region. Otherwise print its rows fresh, prefaced with a blank line.
//
// A thinking placeholder is a footer-only region pinned at submit, before the
// prompt has even round-tripped. A message printed while it is up must land
// ABOVE it in scrollback, so erase the placeholder, print, and repaint it in
// place — otherwise the footer would be stranded above the very message it
// belongs under.
// A message IS the live region only when it starts where the region starts.
// LT alone is not identity: aria.Message.LT carries the TURN id, so every
// message in a turn shares it. Testing LT alone made freezing a one-node
// steering interjection drop the whole streaming region — seventeen rows of
// it — into scrollback, after which the rest of the turn was frozen again and
// the entire post-steer block printed twice.
// A message IS the live region only when it covers the SAME EXTENT. Start
// alone is not identity either: while the agent streams, the open suffix begins
// at the steer's own index, so a one-node steering interjection and a
// four-node streaming region both report From=1. Matching on start made Freeze
// treat the steer as the whole region and dropBelow() nineteen rows of
// in-flight output into scrollback, after which the server reopened past the
// steer and the same body was frozen again.
func (i *Incipit) isLiveRegion(m aria.Message) bool {
	return i.liveTurn != 0 && m.Turn == i.liveTurn &&
		m.From == i.liveFrom && len(m.Nodes) == i.liveCount
}

// overlapsLiveRegion reports whether a closed message covers nodes the live
// region is ALSO showing. The region is then stale: those nodes are being
// delivered as closed messages instead, and the producer will reopen past them.
// Erasing is the only correct response — dropping them to scrollback prints
// them twice, once from the abandoned region and once from the frozen message.
func (i *Incipit) overlapsLiveRegion(m aria.Message) bool {
	return i.liveTurn != 0 && m.Turn == i.liveTurn &&
		m.From+uint64(len(m.Nodes)) > i.liveFrom &&
		m.From < i.liveFrom+uint64(i.liveCount)
}

func (i *Incipit) Freeze(m aria.Message) {
	if i.isLiveRegion(m) {
		if !i.bodyHidden() {
			// Repaint with the role's closer in place of the pinned footer
			// before committing. The footer is CHROME: it belongs to whatever
			// region is currently live, and freezing it verbatim left one copy
			// stranded in scrollback per message — two footers for a single
			// exchange, which is not what the sketch asks for.
			i.paint(i.composeWith(m.Inquiry, m.Nodes, i.closer(m.Role)))
			i.dropBelow()
			i.reset()
			return
		}
		// The live region showed only the footer, so the body was never painted
		// and there is nothing on screen worth keeping. Erase the footer and fall
		// through to print the message in full — otherwise the reply is lost from
		// scrollback entirely, which is what happened at every height below the
		// pager floor.
		io.WriteString(i.term, "\x1b[J")
		i.reset()
	} else if i.overlapsLiveRegion(m) {
		// Stale region: erase it and print the message fresh. The producer
		// reopens past these nodes, so anything kept here would be duplicated.
		io.WriteString(i.term, "\x1b[J")
		i.reset()
	}
	restore, role := i.thinking, i.role
	if restore {
		io.WriteString(i.term, "\x1b[J") // cursor is parked at the live top
		i.reset()
	}
	rows := i.messageRows(m.Inquiry, m.Role, m.Nodes)
	var b strings.Builder
	b.WriteString("\r\n") // leading blank — every message is prefaced with a newline
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	// Trailing separator: the same rule/bookend compose() paints, so a message
	// printed fresh (a committed frame that never had a live region — e.g. a
	// user prompt) is still closed off from what follows.
	if closer := i.closer(m.Role); len(closer) > 0 {
		w, _ := i.term.Size()
		b.WriteString("\r\n")
		for _, s := range closer {
			b.WriteString(clip(s, w))
			b.WriteString("\r\n")
		}
	}
	io.WriteString(i.term, b.String())
	if restore {
		i.OpenThinking(role)
	}
}

// Resume rebuilds the inline view after the transcript pager closes. The
// alt-screen restored whatever partial live region was on screen when the pager
// opened; this clears it, prints the given closed messages to scrollback in full
// (the bookend follows the assistant only), and — if a message is still
// streaming — starts a fresh live region. The cursor lands on a new line below
// everything, so input resumes after the content like `figaro show`.
func (i *Incipit) Resume(closed []aria.Message, open *aria.Message) {
	io.WriteString(i.term, "\x1b[J") // clear the restored partial region
	i.reset()
	for _, m := range closed {
		i.printMessage(m)
	}
	if open != nil && open.Turn != 0 {
		i.Open(*open)
	}
}

// printMessage writes a closed message's rows to scrollback (bookend after an
// assistant message), leaving the cursor on a fresh line below. Each message is
// prefaced with a blank line plus the role header (when configured) — the same
// leading rule Freeze/compose apply.
func (i *Incipit) printMessage(m aria.Message) {
	body := i.messageRows(m.Inquiry, m.Role, m.Nodes)
	if len(body) == 0 {
		return
	}
	rows := append([]string{""}, body...)
	if closer := i.closer(m.Role); len(closer) > 0 {
		w, _ := i.term.Size()
		rows = append(rows, "")
		for _, s := range closer {
			rows = append(rows, clip(s, w))
		}
	}
	io.WriteString(i.term, strings.Join(rows, "\r\n")+"\r\n")
}

// Open paints (or repaints) the open message's blocks as the live region.
func (i *Incipit) Open(m aria.Message) {
	// A thinking placeholder (OpenThinking) is the same on-screen region as the
	// assistant message that follows: adopt its turn in place — no dropBelow, no
	// reset — so the footer painted on submit stays put and the streamed
	// content fills in above it, never orphaning a header/footer to scrollback.
	if i.thinking && m.Role == i.role {
		i.thinking = false
		i.liveTurn, i.liveFrom, i.liveCount, i.liveInquiry = m.Turn, m.From, len(m.Nodes), m.Inquiry
		i.paint(i.compose(m.Nodes))
		return
	}
	if m.Turn != i.liveTurn || m.From != i.liveFrom {
		// A new open message without a prior Freeze: release whatever was live.
		// A thinking placeholder is ERASED rather than released — it is a pinned
		// footer, not content, and dropping it into scrollback would strand the
		// status bar above the very message it describes.
		if i.thinking {
			io.WriteString(i.term, "\x1b[J")
		} else if i.liveTurn != 0 {
			i.dropBelow()
		}
		i.reset()
		i.liveTurn, i.liveFrom = m.Turn, m.From
	}
	i.liveCount = len(m.Nodes)
	i.liveInquiry = m.Inquiry
	i.role = m.Role
	i.paint(i.compose(m.Nodes))
}

// OpenThinking paints an empty live region for a role that has only started —
// its header and status footer — before any content has streamed. The next
// Open(realLT, sameRole, …) adopts this region in place. Used on submit so the
// footer appears immediately rather than waiting for the model's first token.
func (i *Incipit) OpenThinking(role string) {
	if i.liveTurn != 0 {
		i.dropBelow()
	}
	i.reset()
	i.thinking = true
	i.liveTurn = thinkingTurn
	i.role = role
	i.paint(i.compose(nil))
}

// thinkingTurn is the sentinel liveTurn for an OpenThinking placeholder — any
// value a real message LT never takes.
const thinkingTurn = -1

// Tick advances spinner animation and repaints the open message.
func (i *Incipit) Tick(nodes []livedoc.Node) {
	if i.liveTurn == 0 {
		return
	}
	if i.thinking {
		nodes = nil // placeholder region has no content of its own yet
	}
	i.tick++
	i.paint(i.compose(nodes))
}

// Resize repaints the open message at the new width — clearing from the live
// region's top downward only, so scrollback above is untouched.
func (i *Incipit) Resize(nodes []livedoc.Node) {
	if i.liveTurn == 0 {
		return
	}
	io.WriteString(i.term, "\x1b[J") // erase from the live-region top to end of screen
	i.live = nil
	i.vt, i.cur = 0, 0
	i.paint(i.compose(nodes))
}

// LiveHeight is the row count of the open, redrawable region. Zero when nothing
// is live. A viewport shorter than this cannot be repainted in place: the
// terminal scrolls the overflow into native scrollback before our code runs.
func (i *Incipit) LiveHeight() int { return len(i.live) }

// minInlineHeight is the viewport below which the live region draws the footer
// ALONE. It mirrors cli.minPagerHeight deliberately: that constant is the floor
// under which the transcript pager refuses to open, so below it there is no
// escape hatch from the inherent inline limit (docs/ui-stream.md) — a live
// region taller than the viewport scrolls rows into native history, where they
// can never be repainted, stranding half-drawn frames forever.
//
// Drawing only the footer there costs a live preview that was already broken
// and buys back the guarantee that matters: the completed message reaches
// scrollback exactly once, via Freeze.
const minInlineHeight = 10

// bodyHidden reports whether the live region is footer-only at this size.
func (i *Incipit) bodyHidden() bool {
	h := i.viewportHeight()
	return h > 0 && h < minInlineHeight
}

// viewportHeight is the terminal's row count as measured.
func (i *Incipit) viewportHeight() int {
	_, h := i.term.Size()
	return h
}

// clipRows keeps a region from being TALLER THAN THE TERMINAL. Painting more
// rows than exist scrolls the earlier ones into history, where they can never
// be repainted — so at the extreme the suppressed footer (rule + status, two
// rows) overflowed a one-row viewport, stranded a partial frame in scrollback
// and lost the completed reply.
//
// The TAIL survives, because the status line is the row the user needs; the
// rule above it is decoration. h <= 0 means the size is unknown, so clip
// nothing rather than guess.
func clipRows(rows []string, h int) []string {
	if h <= 0 || len(rows) <= h {
		return rows
	}
	return rows[len(rows)-h:]
}

func (i *Incipit) compose(nodes []livedoc.Node) []string {
	return i.composeWith(i.liveInquiry, nodes, i.footer())
}

// composeWith builds the region's rows with an explicit trailer. The LIVE
// region trails the pinned footer; a region being FROZEN trails its role's
// closer instead, because the footer is chrome — committing it would strand a
// status bar in scrollback above the very message that follows it.
func (i *Incipit) composeWith(inquiry string, nodes []livedoc.Node, foot []string) []string {
	w, _ := i.term.Size()
	if i.bodyHidden() {
		rows := make([]string, 0, len(foot))
		for _, s := range foot {
			rows = append(rows, clip(s, w))
		}
		return clipRows(rows, i.viewportHeight())
	}
	body := i.messageRows(inquiry, i.role, nodes)
	// Every message is prefaced with a blank row — frozen into scrollback
	// alongside the rest of the live region.
	rows := make([]string, 0, len(body)+3)
	rows = append(rows, "")
	rows = append(rows, body...)
	if len(foot) > 0 {
		rows = append(rows, "")
		for _, s := range foot {
			rows = append(rows, clip(s, w))
		}
	}
	return rows
}

// messageRows is one message's body: the turn's opening question under the
// input header, the RULE that closes it, then the nodes under the speaker's
// header. It is the ONE place the inquiry is drawn — it is text on the turn,
// not a node, so no node walk can produce it.
//
// The rule appears only when the agent actually says something in this message.
// A question with nothing under it is closed by the message's own closer (see
// Freeze/printMessage), which is the same rule; drawing both would double it.
//
// No header over an empty run: a run whose nodes all render to nothing
// (thinking hidden, minted-but-empty prose, a tool already drawn) must not
// print a header over empty space, and at submit the live region is
// deliberately bodyless — the inquiry has arrived, the reply has not.
func (i *Incipit) messageRows(inquiry, role string, nodes []livedoc.Node) []string {
	rows := i.inquiryRows(inquiry)
	body := i.renderNodes(nodes)
	if len(body) == 0 {
		return rows
	}
	if len(rows) > 0 {
		rows = append(rows, "")
		if r := i.rule(); r != "" {
			rows = append(rows, r)
		}
	}
	if h := i.header(role); h != "" {
		rows = append(rows, h, "")
	}
	return append(rows, body...)
}

// rule is the plain full-width separator, clipped to the terminal. Empty when
// the caller configured none.
func (i *Incipit) rule() string {
	if i.Rule == nil {
		return ""
	}
	w, _ := i.term.Size()
	return clip(i.Rule(), w)
}

// inquiryRows draws the question that opened the turn: the input header, a
// blank, then the text as prose. Same prose renderer the nodes use, so the
// question looks the same whether you watch it arrive or read it back.
func (i *Incipit) inquiryRows(inquiry string) []string {
	if strings.TrimSpace(inquiry) == "" {
		return nil
	}
	w, _ := i.term.Size()
	if w <= 0 {
		w = 80
	}
	var rows []string
	if h := i.header(livedoc.RoleInput); h != "" {
		rows = append(rows, h, "")
	}
	for _, l := range render.Prose(inquiry, w) {
		rows = append(rows, clip(l, w))
	}
	return rows
}

// header returns the role-header line for role (e.g. "❯ input") or "" if no
// Header function is configured or the role has no glyph.
func (i *Incipit) header(role string) string {
	if i.Header == nil {
		return ""
	}
	return i.Header(role)
}

// footer returns the rows pinned at the bottom of the LIVE region. Unlike
// closer(), it does not depend on the role: the status bar is a fixture of the
// view — present at submit, during streaming, at completion, and after a pager
// round-trip. Making it a consequence of which message happened to close is
// exactly what let it vanish when the prompt merged into the turn.
func (i *Incipit) footer() []string {
	if i.Bookend != nil {
		return i.Bookend()
	}
	if i.Rule != nil {
		return []string{i.Rule()}
	}
	return nil
}

// closer returns the rows that close a message of the given role: the two-row
// status bookend after an assistant message, otherwise a plain full-width rule
// (so the user's prompt is still separated from the reply). Empty if neither
// is configured.
func (i *Incipit) closer(role string) []string {
	if role == livedoc.RoleOutput && i.Bookend != nil {
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
		// Minted-but-empty prose/thinking (docs/turn-addressing.md, invariant 6)
		// holds a node id so later ids cannot shift; it draws nothing.
		if n.Type != livedoc.NodeTool && strings.TrimSpace(n.Markdown) == "" {
			continue
		}
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

// AbandonOpen ends the live region without a normal Freeze (no figaro.aria
// close frame arrived). It moves the cursor past the live content and prints
// line on a fresh row as a visual boundary, so the next stream lands on clean
// ground. Use this when the agent dies mid-turn, the user disconnects with
// Ctrl-D, or an interrupt times out.
//
// line is typically a dim rule with a reason label — the caller owns the
// formatting (CLI policy). Without a live region, line is still printed.
func (i *Incipit) AbandonOpen(line string) {
	var b strings.Builder
	if i.liveTurn != 0 {
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
	i.liveTurn, i.liveFrom, i.liveCount, i.liveInquiry = 0, 0, 0, ""
	i.role, i.live, i.vt, i.cur = "", nil, 0, 0
	i.thinking = false
}
