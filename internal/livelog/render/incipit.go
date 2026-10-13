package render

import (
	"fmt"
	"github.com/jack-work/figaro/internal/mark"
	"io"
	"strings"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
)

// NodeView renders one block to terminal rows. Each row must be a single
// physical line of at most width columns.
type NodeView interface {
	Render(n livedoc.Node, width, tick int) []string
}

// diffRange returns the first and last indices where old and next differ
// (-1,-1 if identical).
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

// Incipit renders an aria stream inline, no alternate screen. Closed messages
// reach native scrollback exactly once (Freeze) and are never touched again;
// only the open message is a redrawable region, so a resize never reflows
// committed history.
type Incipit struct {
	term        Terminal
	view        NodeView
	Bookend     func() []string          // closes an assistant message
	Rule        func() string            // closes any other message
	Header      func(role string) string // "" suppresses
	InputHeader func(sender string) string
	Quote       func(m quote.Mention, w int, expanded bool) []string
	// Queued renders accepted prompts not yet in the transcript: live chrome
	// above the bookend, never frozen, since the log does not contain them.
	Queued func() []string

	tick     int
	thinking bool // open region is an OpenThinking placeholder

	// atRule: the last row in scrollback is a bare rule, so the next message
	// draws no top margin.
	atRule bool

	liveTurn    int
	liveFrom    uint64
	liveCount   int // (turn,from,count) is the region's identity
	liveInquiry string
	liveSegs    []aria.InquirySegment
	liveNodes   []livedoc.Node // last composed, so Seal can repaint unasked
	role        string
	live        []string // rows on screen for the open message
	vt          int      // rows of the live region scrolled above the viewport
	cur         int      // cursor row within the live region (0 = top)
}

func NewIncipit(term Terminal, view NodeView) *Incipit {
	return &Incipit{term: term, view: view}
}

// OpenRule prints the rule between the caller's shell prompt and the stream.
func (i *Incipit) OpenRule() {
	if i.Rule == nil {
		return
	}
	w, _ := i.term.Size()
	io.WriteString(i.term, clip(i.Rule(), w)+"\r\n")
	i.atRule = true
}

func (i *Incipit) topMargin() []string {
	if i.atRule {
		return nil
	}
	return []string{""}
}

func (i *Incipit) isLiveRegion(m aria.Message) bool {
	return i.liveTurn != 0 && m.Turn == i.liveTurn &&
		m.From == i.liveFrom && len(m.Nodes) == i.liveCount
}

// overlapsLiveRegion reports a closed message covering nodes the live region
// also shows: the region is stale and must be erased, not dropped to scrollback.
func (i *Incipit) overlapsLiveRegion(m aria.Message) bool {
	return i.liveTurn != 0 && m.Turn == i.liveTurn &&
		m.From+uint64(len(m.Nodes)) > i.liveFrom &&
		m.From < i.liveFrom+uint64(i.liveCount)
}

// Freeze finalizes a closed message: a live region is committed in place,
// anything else is printed fresh.
func (i *Incipit) Freeze(m aria.Message) {
	if i.isLiveRegion(m) {
		if !i.bodyHidden() {
			// The footer is chrome; the role's closer is committed in its place.
			closer, endsInRule := i.closer(m.Role)
			i.paint(i.composeWith(m.Inquiry, m.InquirySegments, m.Nodes, closer))
			i.dropBelow()
			i.reset()
			i.atRule = endsInRule
			return
		}
		// Footer-only region: the body was never painted, so print it in full.
		io.WriteString(i.term, "\x1b[J")
		i.reset()
	} else if i.overlapsLiveRegion(m) {
		io.WriteString(i.term, "\x1b[J")
		i.reset()
	}
	restore, role := i.thinking, i.role
	if restore {
		io.WriteString(i.term, "\x1b[J")
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

// Resume rebuilds the inline view after the transcript pager closes: clears
// the restored partial region, prints the closed tail, reopens any live message.
func (i *Incipit) Resume(closed []aria.Message, open *aria.Message, maxRows int) {
	// CR first: the column \x1b[?1049l restores to is the terminal's choice
	// (microsoft/terminal#381).
	io.WriteString(i.term, "\r\x1b[J")
	i.reset()
	if rows, endsInRule, ok := i.tailRows(closed, maxRows); ok {
		io.WriteString(i.term, strings.Join(rows, "\r\n")+"\r\n")
		i.atRule = endsInRule
	}
	if open != nil && open.Turn != 0 {
		i.Open(*open)
	}
}

// tailRows renders the newest messages back-to-front until maxRows are in
// hand, returned in order; ok is false when nothing renders.
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

// messagePrintRows renders a closed message as it appears in scrollback.
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
	// A thinking placeholder is adopted in place, so its footer stays put.
	if i.thinking && m.Role == i.role {
		i.thinking = false
		i.liveTurn, i.liveFrom, i.liveCount, i.liveInquiry = m.Turn, m.From, len(m.Nodes), m.Inquiry
		i.liveSegs = m.InquirySegments
		i.paint(i.compose(m.Nodes))
		return
	}
	if m.Turn != i.liveTurn || m.From != i.liveFrom {
		// A thinking placeholder is erased rather than released: it is chrome.
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

// OpenThinking paints an empty live region (header and footer) that the next
// Open of the same role adopts in place.
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

// thinkingTurn is the sentinel liveTurn for an OpenThinking placeholder.
const thinkingTurn = -1

// Tick advances spinner animation and repaints the open message.
func (i *Incipit) Tick(nodes []livedoc.Node) {
	if i.liveTurn == 0 {
		return
	}
	if i.thinking {
		nodes = nil
	}
	i.tick++
	i.paint(i.compose(nodes))
}

// Resize repaints the open message at the new width, clearing from the live
// region's top downward only.
func (i *Incipit) Resize(nodes []livedoc.Node) {
	if i.liveTurn == 0 {
		return
	}
	io.WriteString(i.term, "\x1b[J")
	i.live = nil
	i.vt, i.cur = 0, 0
	i.paint(i.compose(nodes))
}

// LiveHeight is the row count of the open region; a shorter viewport cannot
// repaint it in place.
func (i *Incipit) LiveHeight() int { return len(i.live) }

// minInlineHeight is the viewport below which the live region draws the footer
// alone; it must equal cli.minPagerHeight, the floor under which the pager
// refuses to open.
const minInlineHeight = 10

func (i *Incipit) bodyHidden() bool {
	h := i.viewportHeight()
	return h > 0 && h < minInlineHeight
}

func (i *Incipit) viewportHeight() int {
	_, h := i.term.Size()
	return h
}

// clipRows keeps a region from being taller than the terminal: overflow
// scrolls into history where it can never be repainted.
func clipRows(rows []string, h int) []string {
	if h <= 0 || len(rows) <= h {
		return rows
	}
	return rows[len(rows)-h:]
}

func (i *Incipit) compose(nodes []livedoc.Node) []string {
	i.liveNodes = nodes
	return i.composeWith(i.liveInquiry, i.liveSegs, nodes, i.footer())
}

// composeWith builds the region's rows with an explicit trailer: the pinned
// footer while live, the role's closer when freezing.
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

func (i *Incipit) messageRows(inquiry string, segments []aria.InquirySegment, role string, nodes []livedoc.Node) []string {
	w, _ := i.term.Size()
	return Text(i.composer().Message(aria.Message{
		Role: role, Inquiry: inquiry, InquirySegments: segments, Nodes: nodes,
	}, w))
}

func (i *Incipit) composer() Composer {
	return Composer{View: i.view, Header: i.Header, InputHeader: i.InputHeader, Quote: i.Quote, Tick: i.tick,
		Rule: func() string {
			if i.Rule == nil {
				return ""
			}
			return i.Rule()
		}}
}

// footer is the rows pinned at the bottom of the live region. Unlike closer
// it does not depend on the role: the status bar is a fixture of the view.
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

// closer is the rows that close a message of the given role: the bookend after
// an assistant message, otherwise a plain rule.
func (i *Incipit) closer(role string) (rows []string, endsInRule bool) {
	if role == livedoc.RoleOutput && i.Bookend != nil {
		return i.Bookend(), false
	}
	if i.Rule != nil {
		return []string{i.Rule()}, true
	}
	return nil, false
}

// paint line-diffs newRows against the on-screen live region. The cursor
// enters and leaves at the top of the region.
func (i *Incipit) paint(newRows []string) {
	first, last := diffRange(i.live, newRows)
	if first < 0 {
		mark.Mark("frame.quiet", "view", "incipit")
		return
	}
	_, h := i.term.Size()
	var b strings.Builder
	b.WriteString("\x1b[?2026h") // synchronized output
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
	i.vmove(&b, i.vt)
	b.WriteString("\r")
	b.WriteString("\x1b[?2026l")
	io.WriteString(i.term, b.String())
	i.live = newRows
	if mark.Enabled() {
		mark.Mark("frame", "view", "incipit", "bytes", b.Len(), "rows", len(newRows),
			"content", len(newRows) > 0, "tail", true, "changed", last-first+1)
	}
}

func (i *Incipit) vmove(b *strings.Builder, target int) {
	if d := target - i.cur; d > 0 {
		fmt.Fprintf(b, "\x1b[%dB", d)
	} else if d < 0 {
		fmt.Fprintf(b, "\x1b[%dA", -d)
	}
	i.cur = target
}

// Seal ends the live region with its footer committed as it stands now: the
// exit for a turn that ends without a close frame when the footer is the news.
// With no region up the footer is printed fresh.
func (i *Incipit) Seal() {
	if i.liveTurn != 0 {
		i.paint(i.compose(i.liveNodes))
		i.dropBelow()
		i.reset()
		i.atRule = false
		return
	}
	foot := i.footer()
	if len(foot) == 0 {
		return
	}
	// The stanza's opening rule may already be on screen.
	if i.atRule && i.Rule != nil && foot[0] == i.Rule() {
		foot = foot[1:]
	}
	w, _ := i.term.Size()
	var b strings.Builder
	for _, r := range i.topMargin() {
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	for _, s := range foot {
		b.WriteString(clip(s, w))
		b.WriteString("\r\n")
	}
	io.WriteString(i.term, b.String())
	i.atRule = false
}

// AbandonOpen ends the live region without a close frame: the cursor moves
// past the live content and line is printed as a boundary. When the footer
// carries the news, Seal is the right exit.
func (i *Incipit) AbandonOpen(line string) {
	var b strings.Builder
	if i.liveTurn != 0 {
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
	i.atRule = false
}

func (i *Incipit) dropBelow() {
	// The cursor is parked at the region's visible top; rows above the
	// viewport are not on screen to walk past.
	if n := len(i.live) - i.vt; n > 0 {
		io.WriteString(i.term, strings.Repeat("\r\n", n))
	}
}

func (i *Incipit) reset() {
	i.liveTurn, i.liveFrom, i.liveCount, i.liveInquiry, i.liveSegs = 0, 0, 0, "", nil
	i.liveNodes = nil
	i.role, i.live, i.vt, i.cur = "", nil, 0, 0
	i.thinking = false
}
