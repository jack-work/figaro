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

// Incipit renders an aria stream inline: no alternate screen. Closed messages
// are printed to native scrollback exactly once (Freeze) and never touched again;
// only the open message is a live, redrawable region (Open). A resize repaints
// just the open message: the bounded, mutable part: so committed history is
// never reflowed. That is the structural fix for the resize/duplication class:
// the immutability boundary (a frozen message) is also the resize boundary.
type Incipit struct {
	term    Terminal
	view    NodeView
	Bookend func() []string          // closes an assistant message (the two-row status footer)
	Rule    func() string            // closes any other message (a plain full-width rule)
	Header  func(role string) string // printed above each message; "" suppresses
	Sender  func(name string) string // styles a per-segment attribution; nil suppresses
	// Queued renders prompts the agent has accepted but not yet placed in the
	// transcript. They are LIVE CHROME, drawn just above the bookend and never
	// frozen: a queued prompt has not happened yet, so committing it to
	// scrollback would assert something the log does not contain. closer() is
	// deliberately not routed through here for that reason.
	Queued func() []string

	tick     int
	thinking bool // open region is an OpenThinking placeholder (adopted by the next Open)

	// atRule records that the last row already in scrollback is a BARE RULE, and
	// therefore that the next message must draw no top margin of its own.
	atRule bool

	// Open-message live region:
	liveTurn    int
	liveFrom    uint64                // start of the open suffix
	liveCount   int                   // node count of the open suffix; (turn,from,count) is the region's identity
	liveInquiry string                // the turn's opening question, drawn above the nodes
	liveSegs    []aria.InquirySegment // that question split by sender; nil when unattributed
	role        string                // open message's role; selects Bookend (assistant) vs Rule
	live        []string              // rows on screen for the open message
	vt          int                   // rows of the live region scrolled above the viewport
	cur         int                   // cursor row within the live region (0 = top)
}

// NewIncipit returns an inline renderer drawing to term via view.
func NewIncipit(term Terminal, view NodeView) *Incipit {
	return &Incipit{term: term, view: view}
}

// OpenRule prints the rule that separates the caller's shell prompt from the
// stream, and records that the next message sits directly under it.
func (i *Incipit) OpenRule() {
	if i.Rule == nil {
		return
	}
	w, _ := i.term.Size()
	io.WriteString(i.term, clip(i.Rule(), w)+"\r\n")
	i.atRule = true
}

// topMargin is the blank row a message is prefaced with, or nothing when the
// row above is already this message's overline. See Incipit.atRule.
func (i *Incipit) topMargin() []string {
	if i.atRule {
		return nil
	}
	return []string{""}
}

// Freeze finalizes a closed message. If it's the message currently live, its
// rows are already on screen: drop the cursor below them and release the
// region. Otherwise print its rows fresh, prefaced with a blank line.
func (i *Incipit) isLiveRegion(m aria.Message) bool {
	return i.liveTurn != 0 && m.Turn == i.liveTurn &&
		m.From == i.liveFrom && len(m.Nodes) == i.liveCount
}

// overlapsLiveRegion reports whether a closed message covers nodes the live
// region is ALSO showing. The region is then stale: those nodes are being
// delivered as closed messages instead, and the producer will reopen past them.
// Erasing is the only correct response: dropping them to scrollback prints
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
			// stranded in scrollback per message: two footers for a single
			// exchange, which is not what the sketch asks for.
			closer, endsInRule := i.closer(m.Role)
			i.paint(i.composeWith(m.Inquiry, m.InquirySegments, m.Nodes, closer))
			i.dropBelow()
			i.reset()
			i.atRule = endsInRule
			return
		}
		// The live region showed only the footer, so the body was never painted
		// and there is nothing on screen worth keeping. Erase the footer and fall
		// through to print the message in full: otherwise the reply is lost from
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
	rows := i.messageRows(m.Inquiry, m.InquirySegments, m.Role, m.Nodes)
	var b strings.Builder
	for _, r := range i.topMargin() {
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	// Trailing separator: the same rule/bookend compose() paints, so a message
	// printed fresh (a committed frame that never had a live region: e.g. a
	// user prompt) is still closed off from what follows.
	closer, endsInRule := i.closer(m.Role)
	if len(closer) > 0 {
		w, _ := i.term.Size()
		b.WriteString("\r\n")
		for _, s := range closer {
			b.WriteString(clip(s, w))
			b.WriteString("\r\n")
		}
	}
	io.WriteString(i.term, b.String())
	i.atRule = endsInRule
	if restore {
		i.OpenThinking(role)
	}
}

// Resume rebuilds the inline view after the transcript pager closes. The
// alt-screen restored whatever partial live region was on screen when the pager
// opened; this clears it, prints the given closed messages to scrollback (the
// bookend follows the assistant only), and: if a message is still streaming -
// starts a fresh live region. The cursor lands on a new line below everything,
// so input resumes after the content like `figaro show`.
func (i *Incipit) Resume(closed []aria.Message, open *aria.Message, maxRows int) {
	// CR first: the column \x1b[?1049l restores to is the terminal's answer,
	// not ours (microsoft/terminal#381).
	io.WriteString(i.term, "\r\x1b[J") // clear the restored partial region
	i.reset()
	if rows, endsInRule, ok := i.tailRows(closed, maxRows); ok {
		io.WriteString(i.term, strings.Join(rows, "\r\n")+"\r\n")
		i.atRule = endsInRule
	}
	if open != nil && open.Turn != 0 {
		i.Open(*open)
	}
}

// tailRows renders the newest messages back-to-front until maxRows physical
// rows are in hand, then returns them in order clipped to that budget, plus
// the atRule state the last message leaves behind. ok is false when nothing
// renders (every message empty, or none given).
func (i *Incipit) tailRows(closed []aria.Message, maxRows int) (rows []string, endsInRule, ok bool) {
	var chunks [][]string
	total := 0
	for k := len(closed) - 1; k >= 0; k-- {
		atRule := i.atRule
		if k > 0 {
			_, atRule = i.closer(closed[k-1].Role)
		}
		chunk, ends := i.messagePrintRows(closed[k], atRule)
		if len(chunk) == 0 {
			continue
		}
		if !ok {
			endsInRule, ok = ends, true
		}
		chunks = append(chunks, chunk)
		if total += len(chunk); maxRows > 0 && total >= maxRows {
			break
		}
	}
	if !ok {
		return nil, false, false
	}
	for k := len(chunks) - 1; k >= 0; k-- {
		rows = append(rows, chunks[k]...)
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	return rows, endsInRule, true
}

// messagePrintRows renders a closed message as it appears in scrollback
// (bookend after an assistant message), prefaced with a blank line plus the
// role header when configured: the same leading rule Freeze/compose apply.
// atRule says whether the row above is already this message's overline, so no
// top margin is drawn; endsInRule reports the same fact for the message below.
func (i *Incipit) messagePrintRows(m aria.Message, atRule bool) (rows []string, endsInRule bool) {
	body := i.messageRows(m.Inquiry, m.InquirySegments, m.Role, m.Nodes)
	closer, endsInRule := i.closer(m.Role)
	if len(body) == 0 {
		return nil, endsInRule
	}
	if !atRule {
		rows = append(rows, "")
	}
	rows = append(rows, body...)
	if len(closer) > 0 {
		w, _ := i.term.Size()
		rows = append(rows, "")
		for _, s := range closer {
			rows = append(rows, clip(s, w))
		}
	}
	return rows, endsInRule
}

// Open paints (or repaints) the open message's blocks as the live region.
func (i *Incipit) Open(m aria.Message) {
	// A thinking placeholder (OpenThinking) is the same on-screen region as the
	// assistant message that follows: adopt its turn in place: no dropBelow, no
	// reset: so the footer painted on submit stays put and the streamed
	// content fills in above it, never orphaning a header/footer to scrollback.
	if i.thinking && m.Role == i.role {
		i.thinking = false
		i.liveTurn, i.liveFrom, i.liveCount, i.liveInquiry = m.Turn, m.From, len(m.Nodes), m.Inquiry
		i.liveSegs = m.InquirySegments
		i.paint(i.compose(m.Nodes))
		return
	}
	if m.Turn != i.liveTurn || m.From != i.liveFrom {
		// A new open message without a prior Freeze: release whatever was live.
		// A thinking placeholder is ERASED rather than released: it is a pinned
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
	i.liveInquiry, i.liveSegs = m.Inquiry, m.InquirySegments
	i.role = m.Role
	i.paint(i.compose(m.Nodes))
}

// OpenThinking paints an empty live region for a role that has only started -
// its header and status footer: before any content has streamed. The next
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

// thinkingTurn is the sentinel liveTurn for an OpenThinking placeholder, any
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

// Resize repaints the open message at the new width: clearing from the live
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
// escape hatch from the inherent inline limit (skills/figaro/reference/ui-stream.md): a live
// region taller than the viewport scrolls rows into native history, where they
// can never be repainted, stranding half-drawn frames forever.
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
// be repainted: so at the extreme the suppressed footer (rule + status, two
// rows) overflowed a one-row viewport, stranded a partial frame in scrollback
// and lost the completed reply.
func clipRows(rows []string, h int) []string {
	if h <= 0 || len(rows) <= h {
		return rows
	}
	return rows[len(rows)-h:]
}

func (i *Incipit) compose(nodes []livedoc.Node) []string {
	return i.composeWith(i.liveInquiry, i.liveSegs, nodes, i.footer())
}

// composeWith builds the region's rows with an explicit trailer. The LIVE
// region trails the pinned footer; a region being FROZEN trails its role's
// closer instead, because the footer is chrome: committing it would strand a
// status bar in scrollback above the very message that follows it.
func (i *Incipit) composeWith(inquiry string, segments []aria.InquirySegment, nodes []livedoc.Node, foot []string) []string {
	w, _ := i.term.Size()
	if i.bodyHidden() {
		rows := make([]string, 0, len(foot))
		for _, s := range foot {
			rows = append(rows, clip(s, w))
		}
		return clipRows(rows, i.viewportHeight())
	}
	body := i.messageRows(inquiry, segments, i.role, nodes)
	// Every message is prefaced with a blank row: frozen into scrollback
	// alongside the rest of the live region: unless the row above it is
	// already this message's overline (see atRule).
	margin := i.topMargin()
	rows := make([]string, 0, len(body)+len(margin)+2)
	rows = append(rows, margin...)
	rows = append(rows, body...)
	if len(foot) > 0 {
		rows = append(rows, "")
		for _, s := range foot {
			rows = append(rows, clip(s, w))
		}
	}
	return rows
}

// messageRows is one message's body, composed by the one composer every
// surface shares (compose.go). The incipit supplies its chrome hooks and its
// view; the shape of a message is not its to decide.
func (i *Incipit) messageRows(inquiry string, segments []aria.InquirySegment, role string, nodes []livedoc.Node) []string {
	w, _ := i.term.Size()
	return Text(i.composer().Message(aria.Message{
		Role: role, Inquiry: inquiry, InquirySegments: segments, Nodes: nodes,
	}, w))
}

// composer is the incipit's composition: its view, its chrome, its tick.
func (i *Incipit) composer() Composer {
	return Composer{View: i.view, Header: i.Header, Sender: i.Sender, Tick: i.tick,
		Rule: func() string {
			if i.Rule == nil {
				return ""
			}
			return i.Rule()
		}}
}

// footer returns the rows pinned at the bottom of the LIVE region. Unlike
// closer(), it does not depend on the role: the status bar is a fixture of the
// view: present at submit, during streaming, at completion, and after a pager
// round-trip. Making it a consequence of which message happened to close is
// exactly what let it vanish when the prompt merged into the turn.
func (i *Incipit) footer() []string {
	var rows []string
	if i.Queued != nil {
		rows = append(rows, i.Queued()...)
	}
	if i.Bookend != nil {
		return append(rows, i.Bookend()...)
	}
	if i.Rule != nil {
		return append(rows, i.Rule())
	}
	return rows
}

// closer returns the rows that close a message of the given role: the two-row
// status bookend after an assistant message, otherwise a plain full-width rule
// (so the user's prompt is still separated from the reply). Empty if neither
// is configured.
func (i *Incipit) closer(role string) (rows []string, endsInRule bool) {
	if role == livedoc.RoleOutput && i.Bookend != nil {
		return i.Bookend(), false
	}
	if i.Rule != nil {
		return []string{i.Rule()}, true
	}
	return nil, false
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
	// A labelled boundary rule is a terminal marker, not an overline: whatever
	// follows an abandoned turn gets its margin back.
	i.atRule = false
}

func (i *Incipit) dropBelow() {
	// The cursor is parked at the region's visible top (logical row i.vt). When
	// the region scrolled taller than the viewport, its first i.vt rows are above
	// the screen, so the visible span is len-i.vt: using len would leave i.vt
	// blank lines after the bookend.
	if n := len(i.live) - i.vt; n > 0 {
		io.WriteString(i.term, strings.Repeat("\r\n", n))
	}
}

func (i *Incipit) reset() {
	i.liveTurn, i.liveFrom, i.liveCount, i.liveInquiry, i.liveSegs = 0, 0, 0, "", nil
	i.role, i.live, i.vt, i.cur = "", nil, 0, 0
	i.thinking = false
}
