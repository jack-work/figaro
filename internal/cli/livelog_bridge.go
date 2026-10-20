package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/mark"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// livelogTurn drives the inline incipit and the pager over one shared aria.Client;
// only the active view paints.
type livelogTurn struct {
	in     *ldrender.Incipit
	term   *ldrender.ANSITerminal
	client *aria.Client
	view   *ariaView
	tr     *transcript
	status *sessionStatus

	open         aria.Message // the live region; Turn == 0 means nothing is live
	pending      *aria.Message
	finished     bool
	thinkingOpen bool // an OpenThinking placeholder is live and not yet adopted
	failSealed   bool // the failed turn's bar has reached scrollback; two doors lead there
	pace         framePacer

	lastFrozen  sliceCursor // highest slice frozen inline; zero means the pager was entered cold
	pagerClosed []aria.Message

	queued    []queuedItem
	queuedErr string

	// held buffers frames until openInline: a subscription pushes at once, and
	// the question must freeze under its history.
	hold bool
	held []aria.Page

	seeded aria.Page // catch-up page from joining a turn we did not open; the pager folds it Quiet

	catchUp func() // the history read an automatic promotion owes; nil without an RPC client
}

type sliceCursor struct {
	turn int
	from uint64
}

func cursorOf(m aria.Message) sliceCursor { return sliceCursor{turn: m.Turn, from: m.From} }

func (c sliceCursor) after(o sliceCursor) bool {
	if c.turn != o.turn {
		return c.turn > o.turn
	}
	return c.from > o.from
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, figaroID string, startedAt time.Time, status *sessionStatus, bookend func() []string, rule func() string) *livelogTurn {
	view := &ariaView{settings: settings}
	term := ldrender.NewANSITerminal(out, w, h)
	// One audited writer for both surfaces: the pager writes straight to out,
	// not through the Terminal.
	audited := auditWriter(out, term.Size)
	term.SetWriter(audited)
	in := ldrender.NewIncipit(term, view)
	in.Bookend = bookend
	in.Rule = rule
	in.Header = messageHeader
	in.InputHeader = inputHeader
	in.HeadMore = headMore
	in.More = func() bool { return view.settings != nil && view.settings.more }
	in.Quote = func(q quote.Mention, w int, expanded bool) []string { return quoteRows(q, w, expanded) }
	t := &livelogTurn{in: in, term: term, client: aria.NewClient(), view: view, status: status}
	in.Queued = t.queuedRows
	t.tr = newTranscript(audited, w, h, view, t.client, figaroID, startedAt)
	t.tr.setStatus(status)
	t.wireClient()
	return t
}

// The closures capture t, never the client, so they follow a retarget.
func (t *livelogTurn) wireClient() {
	t.client.SetClosedLimit(transcriptTailLimit)
	if t.status != nil {
		t.client.OnMetrics = func(m aria.Metrics) { t.status.update(m) }
	}
	t.client.OnClosed = func(m aria.Message) {
		if t.tr.active {
			if t.lastFrozen.turn != 0 {
				t.pagerClosed = append(t.pagerClosed, m)
			}
			t.tr.render()
		} else if m.Role == livedoc.RoleOutput {
			// A steer splits the run: a turn can close several output
			// regions, and each must freeze.
			if t.pending != nil && cursorOf(*t.pending) != cursorOf(m) {
				t.freezePending()
			}
			t.pending = &m
			if t.finished {
				t.freezePending()
			}
		} else {
			// Output is deferred in pending; input freezes at once.
			// Flush whatever precedes this message first, or a steer
			// is hoisted above the tools before it.
			if t.pending != nil && cursorOf(m).after(cursorOf(*t.pending)) {
				t.freezePending()
			}
			t.in.Freeze(m)
			if c := cursorOf(m); c.after(t.lastFrozen) {
				t.lastFrozen = c
			}
		}
	}
	t.client.OnLive = func(m aria.Message) {
		newOpen := m.Turn != t.open.Turn
		t.open = m
		if m.Role == livedoc.RoleOutput {
			if newOpen {
				t.finished = false
				t.failSealed = false
			}
			t.thinkingOpen = false
			t.status.beginTurn()
		}
		// A turn taller than the viewport cannot render inline without
		// scrolling its live region off-screen.
		if !t.tr.active && t.openOverflows(m.Nodes) {
			t.enterPager()
		}
		if t.tr.active {
			t.tr.render()
		} else {
			t.in.Open(m)
		}
	}
}

const transcriptFrameInterval = time.Second / 120

// How long the painter may trust its own model of the screen before an
// unconditional full frame.
const transcriptResyncInterval = 2 * time.Second

// framePacer owes a trailing flush whenever it refuses a frame: the last frame of
// a burst must reach the screen.
type framePacer struct {
	min     time.Duration
	lock    sync.Locker
	flush   func()
	now     func() time.Time
	after   func(time.Duration, func())
	last    time.Time
	pending bool
}

func (p *framePacer) allow() bool {
	if p.min <= 0 {
		return true
	}
	since := p.now().Sub(p.last)
	if p.last.IsZero() || since >= p.min {
		return true
	}
	if !p.pending {
		p.pending = true
		p.after(p.min-since, p.trailing)
	}
	return false
}

func (p *framePacer) trailing() {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.pending = false
	p.flush()
}

func (p *framePacer) painted() { p.last = p.now() }

// The pacer's trailing render runs on a timer goroutine, so it exists only once
// the render mutex is known.
func (t *livelogTurn) setRenderLock(mu sync.Locker) {
	t.pace.lock = mu
	t.pace.min = transcriptFrameInterval
	if t.pace.now == nil {
		t.pace.now = time.Now
	}
	if t.pace.after == nil {
		t.pace.after = func(d time.Duration, fn func()) { time.AfterFunc(d, fn) }
	}
	t.pace.flush = t.tr.flush
	t.tr.gate = t.pace.allow
	t.tr.painted = t.pace.painted
}

func (t *livelogTurn) beginTranscriptBatch() { t.tr.beginBatch() }
func (t *livelogTurn) endTranscriptBatch()   { t.tr.endBatch() }

// Below minPagerHeight an overflowing turn stays inline and scrolls natively.
const minPagerHeight = 10

func (t *livelogTurn) openOverflows(nodes []livedoc.Node) bool {
	w, h := t.term.Size()
	if h < minPagerHeight {
		return false
	}
	rows := 2 // leading blank + role header
	for k, n := range nodes {
		if k > 0 {
			rows++ // inter-block blank
		}
		rows += len(t.view.Render(n, w, 0))
		if rows >= h {
			return true
		}
	}
	return false
}

// armThinking pins the footer at submit, as sending; the daemon's runtime form
// moves it on.
func (t *livelogTurn) armThinking() {
	if t.tr.active || t.thinkingOpen {
		return
	}
	t.thinkingOpen = true
	t.status.beginTurn()
	t.in.OpenThinking(livedoc.RoleOutput)
}

// Only a read's More.Before may be recorded: a pushed frame's More describes the
// delta window, not the conversation.
func (t *livelogTurn) setMoreBefore(more bool) { t.client.SetMoreBefore(more) }

func (t *livelogTurn) apply(r aria.Page) {
	if t.hold {
		t.held = append(t.held, r)
		return
	}
	t.client.Apply(r, aria.Notify)
}

func (t *livelogTurn) holdFrames() { t.hold = true }

// openInline is the only release of the held frames, and its order is the method.
func (t *livelogTurn) openInline(fetched aria.Page) {
	t.seeded = fetched
	t.seedContext(pageMessages(fetched))
	t.armThinking()
	t.hold = false
	held := t.held
	t.held = nil
	for _, r := range held {
		t.client.Apply(r, aria.Notify)
	}
	if len(held) > 0 && t.tr.active {
		t.tr.render()
	}
}

const preambleMinRows = 6

// seedContext prints a bounded tail above the question and declares all of it frozen.
func (t *livelogTurn) seedContext(msgs []aria.Message) {
	if len(msgs) == 0 || t.tr.active {
		return
	}
	shown := t.fitPreamble(msgs)
	// History closes with the plain rule, not this session's bookend.
	save := t.in.Bookend
	t.in.Bookend = nil
	t.in.Resume(shown, nil, 0)
	t.in.Bookend = save
	// Resume cleared the live region, and with it any footer pinned at submit.
	t.thinkingOpen = false
	if c := cursorOf(msgs[len(msgs)-1]); c.after(t.lastFrozen) {
		t.lastFrozen = c
	}
}

// fitPreamble takes the newest messages that fit in half the viewport; an
// overflowing newest message loses its head.
func (t *livelogTurn) fitPreamble(msgs []aria.Message) []aria.Message {
	w, h := t.term.Size()
	if w <= 0 {
		w = 80
	}
	budget := h / 2
	if budget < preambleMinRows {
		budget = preambleMinRows
	}
	rows, start := 0, len(msgs)
	for k := len(msgs) - 1; k >= 0; k-- {
		n := t.preambleHeight(msgs[k], w)
		if rows+n > budget {
			if start == len(msgs) {
				return []aria.Message{t.clipPreamble(msgs[k], w, budget)}
			}
			break
		}
		rows += n
		start = k
	}
	return msgs[start:]
}

// clipPreamble keeps the inquiry.
func (t *livelogTurn) clipPreamble(m aria.Message, w, budget int) aria.Message {
	for len(m.Nodes) > 0 && t.preambleHeight(m, w) > budget {
		m.Nodes = m.Nodes[1:]
		m.From++
	}
	return m
}

// preambleHeight mirrors Incipit.printMessage and rounds up where a margin is
// conditional.
func (t *livelogTurn) preambleHeight(m aria.Message, w int) int {
	rows := 3 // top margin, the blank before the closer, the closer
	inquiry := strings.TrimSpace(m.Inquiry) != ""
	if inquiry {
		rows += 2 + len(render.Prose(m.Inquiry, w)) // header, blank, prose
	}
	if len(m.Nodes) > 0 {
		if inquiry {
			rows += 2 // blank + rule between question and reply
		}
		rows += 2 // role header, blank
	}
	for k, n := range m.Nodes {
		if k > 0 {
			rows++ // inter-block blank
		}
		rows += len(t.view.Render(n, w, 0))
	}
	return rows
}

func (t *livelogTurn) setDesync(fn func(int)) { t.client.OnDesync = fn }
func (t *livelogTurn) transcriptActive() bool { return t.tr.active }

func (t *livelogTurn) abandon(st turnStatus) {
	if t.finished {
		t.leaveTranscript()
		t.freezePending()
		return
	}
	t.status.setTurn(st)
	t.pending = nil
	t.leaveTranscript()
	t.in.AbandonOpen("")
}

func (t *livelogTurn) tick() {
	// The ticker retires the alert: an expiry must repaint when nothing arrived.
	retired := t.status.retireAlert(time.Now())

	// With nothing animating a tick would recompose and diff the open message
	// for a no-op paint.
	thinking := t.status.advance()
	if !t.client.OpenAnimating() && !thinking && !retired {
		return
	}
	if t.tr.active {
		t.tr.tick++
		t.tr.render()
	} else {
		t.in.Tick(t.open.Nodes)
	}
}

// A viewport shorter than the live region cannot be repainted in place: the
// terminal has already scrolled those rows into scrollback. Escape to the pager.
func (t *livelogTurn) resize(w, h int) {
	t.term.SetSize(w, h)
	if !t.tr.active {
		// The hidden pager must not enter later with a stale width.
		t.tr.setSize(w, h)
	}
	if !t.tr.active && h >= minPagerHeight && t.in.LiveHeight() > h {
		t.enterPager()
	}
	if t.tr.active {
		t.tr.resize(w, h)
		return
	}
	t.in.Resize(t.open.Nodes)
}

// Inline, render repaints only while a region is live: t.open is not cleared at
// freeze, and reopening it reprints a sealed turn.
func (t *livelogTurn) render() {
	if t.tr.active {
		t.tr.render()
	} else if t.open.Turn != 0 && t.in.LiveHeight() > 0 {
		t.in.Open(t.open)
	}
}

// A failed turn seals its bar into scrollback, so the caller posts the alert
// before calling this.
func (t *livelogTurn) finishTurn(reason string) {
	t.status.finishTurn(reason)
	t.finished = true
	if t.tr.active {
		t.tr.render()
		return
	}
	hadPending := t.pending != nil
	t.freezePending()
	failed := strings.HasPrefix(strings.ToLower(reason), "error:")
	if t.thinkingOpen {
		// Nothing adopted the placeholder: drop it, unless the footer is
		// the only place the failure is written.
		t.thinkingOpen = false
		if failed {
			t.in.Seal()
		} else {
			t.in.AbandonOpen("")
		}
	} else if !hadPending && t.open.Turn != 0 && t.open.Role == livedoc.RoleOutput {
		t.in.Open(t.open)
		if failed {
			t.in.Seal()
		}
	} else if failed {
		t.in.Seal()
	}
	if failed {
		t.failSealed = true
	}
}

func (t *livelogTurn) freezePending() {
	if t.pending != nil {
		t.in.Open(*t.pending)
		t.in.Freeze(*t.pending)
		if c := cursorOf(*t.pending); c.after(t.lastFrozen) {
			t.lastFrozen = c
		}
		t.pending = nil
	}
}

func (t *livelogTurn) enterTranscript() { t.enterPager() }

func (t *livelogTurn) enterPager() {
	if t.hasSeed() {
		t.client.SetMoreBefore(t.seeded.More.Before)
		t.client.Apply(t.seeded, aria.Quiet)
	}
	t.tr.enter()
	if !t.hasSeed() && t.catchUp != nil {
		// Callers hold the render lock: the hook may only arm a read, not
		// perform one.
		t.catchUp()
	}
}

func (t *livelogTurn) setCatchUp(fn func()) { t.catchUp = fn }

func (t *livelogTurn) hasSeed() bool { return len(t.seeded.Parts) > 0 }

// inTranscript is read under the render lock.
func (t *livelogTurn) inTranscript() bool { return t.tr.active }

func (t *livelogTurn) transcriptDispatch(ev keyEvent) { t.tr.dispatch(ev) }

func (t *livelogTurn) invalidateTranscriptRows() { t.tr.invalidateRows() }

// showDropped: the pit holds the only copy of the discarded text.
func (t *livelogTurn) showDropped(queue []rpc.QueuedPrompt) {
	rows := make([]pitRow, 0, len(queue)+1)
	rows = append(rows, staticRow(fmt.Sprintf("  interrupted · dropped %s", queueCount(queue))))
	for _, p := range queue {
		rows = append(rows, pitRow{text: "  " + queueRowText(p.Text), yank: p.Text, id: strconv.FormatUint(p.ID, 10)})
	}
	t.tr.pit.showList(pitDropped, "", rows)
	t.tr.render()
	slog.Warn("figaro session", "dropped", len(queue))
}

// report goes to the bar and the log, never the terminal.
func (t *livelogTurn) report(text string) { t.reportAt(text, alertInfo) }

func (t *livelogTurn) reportError(text string) { t.reportAt(text, alertError) }

func (t *livelogTurn) reportAt(text string, level alertLevel) {
	if strings.TrimSpace(text) == "" {
		return
	}
	slog.Warn("figaro session", "report", text)
	t.tr.notes.post(level, t.status.figaroID, text)
	t.status.setNoticeAt(text, level)
	if t.tr.active {
		t.tr.render()
	}
}

func (t *livelogTurn) transcriptSelect(delta int, extend bool) {
	t.tr.selectNode(delta, extend)
	t.tr.render()
}

func (t *livelogTurn) transcriptJumpQuestion(delta int) {
	t.tr.jumpTurn(delta)
	t.tr.render()
}

// Asked before acting, so a click on chrome does not cancel a search prompt being
// typed.
func (t *livelogTurn) transcriptClickable(row int) bool {
	return t.tr.active && t.tr.clickable(row)
}

func (t *livelogTurn) transcriptClick(row int, extend bool) bool {
	if !t.tr.active {
		return false
	}
	// The click resolves against the frame the panel was drawn over: the frame
	// the user clicked.
	acted := t.tr.clickAt(row, extend)
	if acted {
		t.tr.closePanels()
		t.tr.render()
	}
	return acted
}

func (t *livelogTurn) transcriptSelectionPlan() (selectionCopyPlan, bool) {
	return t.tr.selectionPlan()
}

func (t *livelogTurn) clearTranscriptSelection() {
	t.tr.clearSelection()
	t.tr.render()
}

// leaveTranscript is idempotent.
func (t *livelogTurn) leaveTranscript() {
	if !t.tr.active {
		return
	}
	t.tr.leave()
	t.flushTail()
	// The flushed tail closes under a plain rule; a failed turn still owes its
	// bar. Re-post: the alert may have retired.
	if t.finished && !t.failSealed && t.status.lastTurnFailed() {
		if reason := t.status.failure(); reason != "" {
			t.status.setNoticeAt(reason, alertError)
		}
		t.in.Seal()
		t.failSealed = true
	}
	t.status.setNotice("")
}

// One turn of tool output is routinely thousands of rows; the message-level bounds
// are not ones a reader can feel.
const scrollbackTailRows = 100

// flushTail prints what streamed while the pager was up, past the freeze boundary,
// or past the last turn's start on a cold entry.
func (t *livelogTurn) flushTail() {
	v := t.client.View()

	// A turn reaches scrollback as several slices sharing a turn with a rising
	// From: key on both.
	cold := t.lastFrozen.turn == 0
	coldFrom := 0
	if cold {
		coldFrom = lastTurnStart(v)
	}
	want := func(m aria.Message) bool {
		if cold {
			return m.Turn >= coldFrom
		}
		return cursorOf(m).after(t.lastFrozen)
	}

	var closed []aria.Message
	seen := make(map[sliceCursor]bool)
	for _, m := range append(append([]aria.Message(nil), t.pagerClosed...), v.Closed...) {
		if c := cursorOf(m); want(m) && !seen[c] {
			closed = append(closed, m)
			seen[c] = true
		}
	}
	t.pagerClosed = nil
	var open *aria.Message
	if v.Open != nil && (cold && v.Open.Turn >= coldFrom || !cold && v.Open.Turn >= t.lastFrozen.turn) {
		open = v.Open
	}
	if len(closed) == 0 && open == nil {
		return
	}
	sort.SliceStable(closed, func(i, j int) bool { return cursorOf(closed[j]).after(cursorOf(closed[i])) })
	t.in.Resume(closed, open, scrollbackTailRows)
	if len(closed) > 0 {
		t.lastFrozen = cursorOf(closed[len(closed)-1])
	}
}

func lastTurnStart(v aria.View) int {
	for k := len(v.Closed) - 1; k >= 0; k-- {
		if v.Closed[k].Role == livedoc.RoleInput {
			return v.Closed[k].Turn
		}
	}
	if v.Open != nil {
		return v.Open.Turn
	}
	if n := len(v.Closed); n > 0 {
		return v.Closed[n-1].Turn
	}
	return 0
}

func (t *livelogTurn) transcriptScroll(delta int) { t.tr.scrollBy(delta) }

func (t *livelogTurn) transcriptMode() keyMode { return t.tr.mode() }

// Page fetches run off-lock.
func (t *livelogTurn) transcriptPageCursor() (transcriptPageRequest, bool) {
	return t.tr.pageCursor()
}

func (t *livelogTurn) transcriptApplyPage(req transcriptPageRequest, page aria.Page) {
	t.tr.applyPage(req, page)
}

func (t *livelogTurn) setHistoryFetcher(f aria.Fetcher) { t.client.SetFetcher(f) }

// fillGap runs on the prefetch worker, off the render lock.
func (t *livelogTurn) fillGap(ctx context.Context, g aria.Gap) error {
	return t.client.Ensure(ctx, g.From, g.To)
}

func (t *livelogTurn) transcriptFilled() {
	t.tr.invalidateWindow()
	t.tr.settle()
	t.tr.jumpAdvance()
	t.tr.render()
}
func (t *livelogTurn) transcriptSearchingHistory() bool { return t.tr.searchingHistory() }
func (t *livelogTurn) transcriptHistorySearch() (string, bool) {
	if t.tr.search == nil {
		return "", false
	}
	return t.tr.search.query, true
}

func (t *livelogTurn) transcriptPageFailed() {
	t.tr.finishSearch(false)
	t.tr.abandonJump("")
	t.tr.render()
}

func (t *livelogTurn) setQueuedFetch(fn func()) { t.tr.queuedFetch = fn }

func (t *livelogTurn) setTranscriptQueued(prompts []queuedItem, errMsg string) bool {
	if errMsg == t.queuedErr && sameQueue(t.queued, prompts) {
		return false
	}
	t.setQueued(prompts, errMsg)
	return true
}

type ariaView struct {
	settings *renderSettings
	// gesture is true only on the pager. Composer.Expanded == nil means "draw
	// the fullest form", which is right for output and wrong for arguments,
	// whose collapsed form is a live window.
	gesture bool
}

func pagerView(v ldrender.NodeView) ldrender.NodeView {
	av, ok := v.(*ariaView)
	if !ok {
		return v
	}
	c := *av
	c.gesture = true
	return &c
}

func (v *ariaView) Render(n livedoc.Node, width, tick int) []string {
	return v.RenderExpanded(n, width, tick, true)
}

// fullOutput decides both of a tool's collapsible parts.
func (v *ariaView) RenderExpanded(n livedoc.Node, width, tick int, fullOutput bool) []string {
	expand := v.gesture && fullOutput
	cap := nodeBashCapDefault
	if expand {
		cap = nodeOutputUnlimited
	}
	return renderNode(n, width, cap, uint64(tick), expand)
}

func (t *livelogTurn) openRule() { t.in.OpenRule() }

func (t *livelogTurn) setQueued(prompts []queuedItem, errMsg string) {
	t.queued, t.queuedErr = prompts, errMsg
	// The snapshot lands before the refresh: the pit builds its rows from this
	// copy.
	t.tr.queued = t.queued
	t.tr.queuedErr = t.queuedErr
	t.tr.queuedRows = t.queuedRows()
	if len(prompts) > 0 || errMsg != "" {
		t.tr.showQueuedAuto(true)
	} else {
		t.tr.showQueuedAuto(false)
	}
}

// State is part of the row: a message moving from queued to committing must repaint.
func sameQueue(a, b []queuedItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].id != b[i].id || a[i].text != b[i].text || a[i].state != b[i].state {
			return false
		}
	}
	return true
}

// queuedRows is the one rendering of the queue for the inline trailer and the
// pager's panel.
func (t *livelogTurn) queuedRows() []string {
	if len(t.queued) == 0 && t.queuedErr == "" {
		return nil
	}
	w, h := t.term.Size()
	rows := []string{"", term.Dim(clipToWidth("↳ queued messages", w))}
	if t.queuedErr != "" {
		return append(rows, term.Dim(clipToWidth("   "+t.queuedErr, w)))
	}
	max := queuedRowsMax
	if h > 0 && h/3 < max {
		max = h / 3
	}
	for i, q := range t.queued {
		p := q.text
		if i >= max {
			rows = append(rows, term.Dim(clipToWidth(
				fmt.Sprintf("   … and %d more", len(t.queued)-i), w)))
			break
		}
		rows = append(rows, term.Dim(clipToWidth(
			fmt.Sprintf("   %s %d. %s", q.mark(), i+1, firstLineTrim(p)), w)))
	}
	return rows
}

// The live region may not exceed the viewport; the trailer is bounded so a long
// queue cannot push the reply off the top.
const queuedRowsMax = 5

func (t *livelogTurn) invalidateTranscriptWindow() { t.tr.invalidateWindow() }

// transcriptShowsGap reads the index the render built: it is asked on every frame.
func (t *livelogTurn) transcriptShowsGap() bool {
	if !t.tr.active {
		return false
	}
	for k := range t.tr.index.entries {
		if t.tr.index.entries[k].isGap() {
			return true
		}
	}
	return false
}

func (t *livelogTurn) keptScroll() bool { return t.tr.kept }

func (t *livelogTurn) renderNow() {
	if t.tr.active {
		t.tr.renderNow()
		return
	}
	t.render()
}

// retarget keeps the turns below base, the first turn the two arias do not share.
// Called with the render lock held.
func (t *livelogTurn) retarget(figaroID string, status *sessionStatus, base int) seedPlan {
	// Always a new client: the one held may be parked, and truncating it in
	// place would rewrite a parked aria.
	kept := 0
	t.client = t.client.CloneBelow(base)
	kept = t.client.Count()
	t.status = status
	t.wireClient()
	mark.Mark("hop.retain", "to", figaroID, "base", base, "kept", kept)

	t.open = aria.Message{}
	t.pending = nil
	t.finished = false
	t.thinkingOpen = false
	t.lastFrozen = sliceCursor{}
	t.pagerClosed = nil
	t.queued, t.queuedErr = nil, ""
	// The hold itself is the caller's.
	t.held = nil
	t.seeded = aria.Page{}

	t.tr.retarget(t.client, figaroID, status, base)
	// The floor is what the clone holds, not the divergence: a clone drops an
	// open turn, and a read floored above it leaves a hole nothing asks for.
	floor := base
	if first, ok := t.client.FirstMissing(); ok && first < floor {
		floor = first
	}
	switch {
	case t.tr.kept:
		return seedPlan{kind: seedSuffix, from: floor}
	case base > 0:
		return seedPlan{kind: seedTail, from: floor}
	}
	return seedPlan{kind: seedWindow}
}

func (t *livelogTurn) setCommandRunner(fn func(string)) { t.tr.command = fn }

func (t *livelogTurn) setTranscriptCmdOut(title string, rows []pitRow) {
	t.tr.setCmdOut(title, rows)
}

func (t *livelogTurn) setCommandCompleter(fn func(string) []string) { t.tr.completer = fn }

type queuedItem struct {
	id    uint64
	text  string
	state rpc.QueueState
}

func (q queuedItem) mark() string {
	if q.state == rpc.QueueStateCommitting {
		return "→"
	}
	return "·"
}

// adopt puts a parked aria back on screen. Called with the render lock held.
func (t *livelogTurn) adopt(figaroID string, status *sessionStatus, p *parkedSubject, base int) seedPlan {
	// A window inside the shared prefix stays put; the remembered position
	// applies only without overlap.
	here := t.tr.keepsScroll(base)
	t.client = p.client
	t.status = status
	t.wireClient()
	t.open = aria.Message{}
	t.pending = nil
	t.finished = false
	t.thinkingOpen = false
	t.lastFrozen = sliceCursor{}
	t.pagerClosed = nil
	t.queued, t.queuedErr = nil, ""
	t.held = nil
	t.seeded = aria.Page{}
	t.tr.adopt(p, status, here, base)
	from := 0
	if tail, ok := t.client.TailFrom(1); ok {
		// The last held turn may have been open when we left: re-read it.
		from = int(tail.Turn)
	}
	plan := seedPlan{kind: seedTail, from: from}
	if t.tr.kept {
		// Parked in history: nothing is owed unless the store claims more
		// above or has a hole; only folding a page corrects a wrong claim.
		plan = seedPlan{kind: seedNothing, from: from}
		switch {
		case t.client.MoreAfter():
			plan = seedPlan{kind: seedTail, from: from}
		case !t.client.Contiguous(t.tr.from):
			plan = seedPlan{kind: seedSuffix, from: max(base, 1)}
		}
	}
	mark.Mark("hop.adopt", "to", figaroID, "msgs", t.client.Count(),
		"from", from, "kind", plan.kind, "here", here)
	return plan
}
