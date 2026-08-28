package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// livelogTurn renders the aria-read wire. By default it uses the incipit-freeze
// renderer (closed messages freeze to scrollback once; the open message is the one
// live region). Ctrl-T toggles a full-screen transcript pager (see transcript.go)
// that shares the same aria.Client model, so both render the same conversation;
// only the active view paints. Messages that close while the pager is up are
// queued and flushed to the inline scrollback on exit, so nothing is lost.
type livelogTurn struct {
	in     *ldrender.Incipit
	term   *ldrender.ANSITerminal
	client *aria.Client
	view   *ariaView
	tr     *transcript
	status *sessionStatus

	// open is the live region as the client last reported it: the open turn's
	// suffix, its offset, its voice, and the turn's inquiry. Turn == 0 means
	// nothing is live.
	open         aria.Message
	pending      *aria.Message
	finished     bool
	thinkingOpen bool // an OpenThinking placeholder is live and not yet adopted
	pace         framePacer

	// lastFrozen is the highest SLICE incipit has committed to native scrollback
	// inline (via Freeze). It marks the flush boundary: on leaving the pager,
	// everything past it is (re)printed to scrollback, so the turn you watched in
	// the pager is left behind like a normal command. A zero lt means nothing was
	// frozen inline (we entered the pager cold, e.g. `figaro listen`).
	lastFrozen  sliceCursor
	pagerClosed []aria.Message

	// queued is the agent's accepted-but-unplaced prompts, as figaro.queued
	// last reported them. Shown, never echoed; see setQueued.
	//
	// IT KEEPS THE IDS. This used to be []string, flattened at the fetch,
	// because the panel could only ever draw a queue -- and `figaro queue rm`
	// addresses a message BY ID. The moment the pit could act on a row (`x`
	// to drop, `y` to yank) the flattening became the thing standing between a
	// reader and the queue they are looking at.
	queued    []queuedItem
	queuedErr string

	// held buffers pages while the opening of the session is being decided.
	// Frames arrive on the notify pump the instant the connection is up, so
	// without a hold the question can freeze to scrollback BEFORE the history it
	// follows is printed: the one ordering the preamble exists to establish.
	// Holding is a few milliseconds at the very start of a session and nothing
	// is dropped: openInline applies every held page in arrival order.
	hold bool
	held []aria.Page

	// seeded is the catch-up page fetched when we joined a turn we did NOT open.
	// The inline view prints a bounded slice of it (seedContext); the PAGER gets
	// the whole set, merged into the client's store when it opens, so entering it
	//: by Ctrl-T or by an overflow auto-enter: renders that history with no
	// round trip of its own. One fetch, two surfaces. It is deliberately NOT
	// APPLIED to aria.Client: a page folded through Apply comes back through
	// OnClosed and re-freezes history into scrollback, which is the whole trap
	// this design is built around. Merge is the silent door.
	seeded      []aria.Message
	seedExtents map[int]uint64
	seedMore    bool

	// catchUp is the history read owed by a pager that opens WITHOUT a seed -
	// i.e. by one of the two automatic promotions. Armed by the session
	// (setCatchUp); nil in tests and in any view that has no RPC client.
	catchUp func()
}

// sliceCursor addresses one pager unit: the turn and the node offset within it.
type sliceCursor struct {
	turn int
	from uint64
}

func cursorOf(m aria.Message) sliceCursor { return sliceCursor{turn: m.Turn, from: m.From} }

// after reports whether c comes strictly after o in reading order.
func (c sliceCursor) after(o sliceCursor) bool {
	if c.turn != o.turn {
		return c.turn > o.turn
	}
	return c.from > o.from
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, figaroID string, startedAt time.Time, status *sessionStatus, bookend func() []string, rule func() string) *livelogTurn {
	view := &ariaView{settings: settings}
	term := ldrender.NewANSITerminal(out, w, h)
	// FIGARO_WIDTH_AUDIT: report any row written past the viewport, from inside
	// the process, in the terminal where it actually happens. Off by default and
	// free when off. See width_audit.go for why the detector had to move here.
	// One audited writer for BOTH surfaces. The pager writes straight to `out`
	// rather than through the Terminal, so auditing only the incipit left the
	// half the reporter was looking at uninstrumented.
	audited := auditWriter(out, term.Size)
	term.SetWriter(audited)
	in := ldrender.NewIncipit(term, view)
	in.Bookend = bookend
	in.Rule = rule
	in.Header = messageHeader
	// Attribution rides in the dim register block timestamps and tool
	// durations use, so a sender reads as metadata rather than as content.
	in.Sender = dimSender
	t := &livelogTurn{in: in, term: term, client: aria.NewClient(), view: view, status: status}
	in.Queued = t.queuedRows // the queue is live chrome in the inline view too
	t.tr = newTranscript(audited, w, h, view, t.client, figaroID, startedAt)
	if status != nil {
		t.tr.status = status
	}
	t.wireClient()
	return t
}

// wireClient installs the retention limit and the three folds on t.client.
// Extracted from newLivelogTurn so that RETARGETING -- pointing this renderer
// at a different aria -- can mint a fresh client and wire it the same way,
// instead of keeping a second copy of the fold in the switch path. The closures
// capture t, never the client, so they follow the retarget by construction.
func (t *livelogTurn) wireClient() {
	t.client.SetClosedLimit(transcriptTailLimit)
	if t.status != nil {
		t.client.OnMetrics = t.status.update
	}
	t.client.OnClosed = func(m aria.Message) {
		if t.tr.active {
			if t.lastFrozen.turn != 0 {
				t.pagerClosed = append(t.pagerClosed, m)
			}
			t.tr.render() // transcript renders from the shared client model
		} else if m.Role == livedoc.RoleOutput {
			// A steer SPLITS the agent's run, so a turn can close several output
			// regions. pending holds one; overwriting it silently discarded every
			// region but the last, which is how three of four tool calls vanished
			// from a steered turn while sitting correctly in the IR. Freeze the
			// outgoing region before taking the new one.
			if t.pending != nil && cursorOf(*t.pending) != cursorOf(m) {
				t.freezePending()
			}
			t.pending = &m
			if t.finished {
				t.freezePending()
			}
		} else {
			// Output is DEFERRED in pending; input freezes immediately. So an
			// input message arriving while an output region is still pending would
			// overtake it, and the same turn told two different stories: incipit
			// hoisted a steer above the four tool calls that preceded it, while
			// `fig show` and the pager placed it correctly after the first two.
			// Turns() is a pure function of the message list, so the live path may
			// not reorder what the projection fixed: the projection is
			// authoritative. Flush anything that comes BEFORE this message first.
			if t.pending != nil && cursorOf(m).after(cursorOf(*t.pending)) {
				t.freezePending()
			}
			t.in.Freeze(m) // incipit: freeze to native scrollback
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
			}
			t.thinkingOpen = false // adopted by real content
			t.status.beginTurn()
		}
		// A turn taller than the viewport can't render inline without scrolling
		// its own live region off-screen; move it to the scrollable pager
		// instead (the user can read/scroll/select there). Auto-entered once,
		// it stays until closed, flushing just the last turn to scrollback.
		if !t.tr.active && t.openOverflows(m.Nodes) {
			t.enterPager() // with the catch-up page, if we joined a turn
		}
		if t.tr.active {
			t.tr.render()
		} else {
			t.in.Open(m)
		}
	}
}

// transcriptFrameInterval is the pager's frame-rate ceiling. Live aria frames
// arrive far faster than a terminal can usefully show them, a streaming tool
// can push dozens of deltas per second, each of which used to trigger a full
// repaint of the pager. 120 fps is above anything a human resolves and well
// above any terminal's own refresh, so nothing is lost by refusing to draw
// more often than this.
const transcriptFrameInterval = time.Second / 120

// transcriptResyncInterval is how long the painter may trust its own model of
// the screen before re-earning it with an unconditional full frame.
const transcriptResyncInterval = 2 * time.Second

// framePacer is the transcript's frame-rate gate. It answers "may I paint
// now?" and, when the answer is no, owes a trailing flush so the settled state
// always reaches the screen: dropping the LAST frame of a burst is the one
// failure mode a rate limiter must not have.
type framePacer struct {
	min     time.Duration
	lock    sync.Locker
	flush   func()
	now     func() time.Time
	after   func(time.Duration, func())
	last    time.Time
	pending bool
}

// allow implements the transcript's gate.
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

// painted records that a frame reached the terminal, starting the budget.
func (p *framePacer) painted() { p.last = p.now() }

// setRenderLock arms the frame-rate ceiling. It is deliberately opt-in: the
// pacer's trailing render runs on a timer goroutine, so it may only exist once
// the caller has told us which mutex serializes the renderer. Without it the
// pager behaves exactly as before (paint on every event).
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

// beginTranscriptBatch/endTranscriptBatch coalesce a burst of input into one
// frame; see (*transcript).beginBatch.
func (t *livelogTurn) beginTranscriptBatch() { t.tr.beginBatch() }
func (t *livelogTurn) endTranscriptBatch()   { t.tr.endBatch() }

// minPagerHeight floors the auto-pager: below this viewport height an
// overflowing turn stays inline and scrolls natively rather than yanking a tiny
// pane into the full-screen pager.
const minPagerHeight = 10

// openOverflows reports whether the open turn's rendered height reaches the
// viewport, so it belongs in the scrollable pager rather than the inline live
// region.
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

// armThinking pins the footer the instant a submit is accepted: before the
// prompt has round-tripped, before the model's first token. The footer is a
// permanent fixture of the view, so it must not wait on the stream.
func (t *livelogTurn) armThinking() {
	if t.tr.active || t.thinkingOpen {
		return
	}
	t.thinkingOpen = true
	t.status.beginTurn()
	t.in.OpenThinking(livedoc.RoleOutput)
}

// setMoreBefore records the wire's answer to "is there anything before this
// page" (Page.More.Before) on the ONE owner. The pager reads it back as "can I
// still page older history"; nothing else may set it, because nothing else
// knows, a PUSHED frame's More describes the delta window, not the
// conversation.
func (t *livelogTurn) setMoreBefore(more bool) { t.client.SetMoreBefore(more) }

func (t *livelogTurn) apply(r aria.Page) {
	if t.hold {
		t.held = append(t.held, r)
		return
	}
	t.client.Apply(r)
}

// holdFrames buffers applied pages until openInline. Armed before the
// connection is dialed, released once the opening of the session is decided.
func (t *livelogTurn) holdFrames() { t.hold = true }

// openInline places the opening of an inline session in the ONE order that
// works, and is the only way to release the held frames: the order is not a
// convention to remember at the call site, it is the method.
func (t *livelogTurn) openInline(fetched historyPage) {
	// The pager's copy: printed or not, the fetch is kept (and merged into the
	// store when the pager opens: see enterPager).
	t.seeded, t.seedExtents, t.seedMore = fetched.msgs, fetched.extents, fetched.more
	t.seedContext(fetched.msgs) // no-op when there is nothing to orient with
	t.armThinking()             // no-op in the pager, and no-op if already pinned
	t.hold = false
	held := t.held
	t.held = nil
	for _, r := range held {
		t.client.Apply(r)
	}
	// The pager draws from the shared model rather than from the event, so a
	// page that finalized nothing would leave it showing the pre-release state.
	if len(held) > 0 && t.tr.active {
		t.tr.render()
	}
}

// preambleMinRows floors the opening preamble's row budget, so a short pane
// still gets some context rather than none.
const preambleMinRows = 6

// seedContext prints a bounded tail of the conversation ABOVE this session's
// question, and declares ALL of the history it was given already committed.
func (t *livelogTurn) seedContext(msgs []aria.Message) {
	if len(msgs) == 0 || t.tr.active {
		return
	}
	shown := t.fitPreamble(msgs)
	// The bookend is THIS session's status line; hanging it under a reply from
	// last week would date-stamp old content with a live turn's metrics. History
	// closes with the plain rule instead.
	save := t.in.Bookend
	t.in.Bookend = nil
	t.in.Resume(shown, nil, 0) // fitPreamble already bounded this to the viewport
	t.in.Bookend = save
	// Resume cleared the live region, which is where a footer pinned at submit
	// was living. Say so, or armThinking would think one is still up and the
	// session would run on with no footer at all.
	t.thinkingOpen = false
	if c := cursorOf(msgs[len(msgs)-1]); c.after(t.lastFrozen) {
		t.lastFrozen = c
	}
}

// fitPreamble takes the newest messages that fit in half the viewport. The
// preamble is orientation, not a transcript: the pager is a Ctrl-T away: so
// it must never push the question the user just asked off the top of the
// screen. When the newest message alone overflows, its HEAD is dropped: the
// rows nearest the new prompt are the ones worth keeping.
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

// clipPreamble drops leading nodes until the message fits the budget. It keeps
// the inquiry: the question is the single most orienting row on the screen,
// and a turn whose question is gone reads as someone else's output.
func (t *livelogTurn) clipPreamble(m aria.Message, w, budget int) aria.Message {
	for len(m.Nodes) > 0 && t.preambleHeight(m, w) > budget {
		m.Nodes = m.Nodes[1:]
		m.From++
	}
	return m
}

// preambleHeight is the row count Incipit.printMessage will emit for m. It
// counts the same pieces the renderer draws: the top margin, the question
// block, the rule between the voices, the role header, the blocks and their
// separators, and the closing rule, and rounds UP where the margin is
// conditional, because a preamble that overshoots its budget is the failure
// mode that pushes the new question off the screen.
func (t *livelogTurn) preambleHeight(m aria.Message, w int) int {
	rows := 3 // top margin, the blank before the closer, the closer itself
	inquiry := strings.TrimSpace(m.Inquiry) != ""
	if inquiry {
		rows += 2 + len(render.Prose(m.Inquiry, w)) // header, blank, prose
	}
	if len(m.Nodes) > 0 {
		if inquiry {
			rows += 2 // the blank + rule that separate the question from the reply
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

// abandon closes a live region left mid-turn: flush the pager's tail, park
// below the region, set the footer's status. Nothing is printed past the
// status bookend: it already says "disconnected".
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
	// THE TICKER RETIRES THE ALERT, and this is the whole of the status bar's
	// liveness. An alert expires on a clock, so something has to repaint when
	// NOTHING arrived -- otherwise it lingers until the next keystroke, which
	// reads as randomness rather than as a timeout. This ticker is already
	// alive and already holds the render lock, so it costs one comparison per
	// frame and needs no second scheduler and no teardown path.
	retired := t.status.retireAlert(time.Now())

	// Only a running tool's spinner needs the periodic repaint. With nothing
	// animating the tick would recompose + diff the whole open message every
	// frame for a no-op paint: pure waste. Content changes still repaint via
	// the OnLive/OnClosed hooks, so gating here is invisible. (The transcript
	// branch already did this; the inline branch didn't.)
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

// resize repaints for the new geometry, escaping to the pager when the shrink
// is destructive. A viewport shorter than the live region is the one case
// inline drawing cannot fix: the terminal scrolls those rows into native
// scrollback before our code runs, so they are unreachable for in-place
// repaint. The pager has no live region to lose.
func (t *livelogTurn) resize(w, h int) {
	t.term.SetSize(w, h)
	if !t.tr.active {
		// Tell the pager even while it is hidden: it may enter itself on the
		// very next frame (below), or minutes later when the live region
		// outgrows the viewport, and it must not enter with a stale width.
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

// render repaints the active view (e.g. after a verbosity toggle).
func (t *livelogTurn) render() {
	if t.tr.active {
		t.tr.render()
	} else if t.open.Turn != 0 {
		t.in.Open(t.open)
	}
}

func (t *livelogTurn) finishTurn(reason string) {
	t.status.finishTurn(reason)
	t.finished = true
	if t.tr.active {
		t.tr.render()
		return
	}
	hadPending := t.pending != nil
	t.freezePending()
	if t.thinkingOpen {
		// The turn ended before any assistant content adopted the thinking
		// placeholder (e.g. an immediate error). Drop it so nothing prints
		// over the live footer region.
		t.thinkingOpen = false
		t.in.AbandonOpen("")
	} else if !hadPending && t.open.Turn != 0 && t.open.Role == livedoc.RoleOutput {
		t.in.Open(t.open)
		if strings.HasPrefix(strings.ToLower(reason), "error:") {
			t.in.AbandonOpen("")
		}
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

// enterTranscript switches to the full-screen pager (the caller has already
// caught the model up via figaro.read so it shows full history).
func (t *livelogTurn) enterTranscript() { t.enterPager() }

// enterPager opens the transcript on whatever catch-up page the inline view
// already fetched. That is the second half of the fetch's job: a prompt that
// lands on a turn it did not open buys a little context, the inline view
// prints a bounded slice of it, and the pager opens on the whole of it: no
// read, and that much less to page in.
func (t *livelogTurn) enterPager() {
	t.client.Merge(t.seeded, t.seedExtents)
	if len(t.seeded) > 0 {
		// What the wire said about the beginning, kept where the wire's answer
		// is. The pager reads it back as "can I still page older history" rather
		// than latching a bit of its own.
		t.client.SetMoreBefore(t.seedMore)
	}
	t.tr.enter()
	if len(t.seeded) == 0 && t.catchUp != nil {
		// Fires after the frame, and never blocks: the callers of this method
		// hold the render lock (the frame path and the resize handler), so the
		// hook may only arm a read, not perform one.
		t.catchUp()
	}
}

// setCatchUp arms the history read the automatic promotions owe (see
// enterPager). Wired by the session, which owns the RPC client, for the same
// reason setQueuedFetch and setHistoryFetcher are. Left nil the hook is simply
// absent: which is what every renderer test wants.
func (t *livelogTurn) setCatchUp(fn func()) { t.catchUp = fn }

// hasSeed reports whether the pager can open on history already in hand: the
// input loop asks so it can skip its blocking catch-up read.
func (t *livelogTurn) hasSeed() bool { return len(t.seeded) > 0 }

// inTranscript reports whether the pager is up. Read under the render lock.
func (t *livelogTurn) inTranscript() bool { return t.tr.active }

// transcriptDispatch routes one decoded keystroke to the locked transcript.
// One path in, whatever the key's encoding was: see (*transcript).dispatch.
func (t *livelogTurn) transcriptDispatch(ev keyEvent) { t.tr.dispatch(ev) }

func (t *livelogTurn) invalidateTranscriptRows() { t.tr.invalidateRows() }

// report is where trouble goes: an error reason, a provider hint, an interrupt
// notice. TWO DESTINATIONS, AND THE TERMINAL IS NOT ONE OF THEM.
//
// It used to have three: the bar, a `pendingReport` list REPRINTED to the
// shell on the way out, and a direct write to stderr when the pager was down.
// That is how hanging up and then disconnecting printed the same sentence
// twice -- "hanging up: staying attached", then "hung up: listening", then
// both again as the pager left -- and why leaving a session was never quiet.
// Gluck, 2026-08-28: "there should not be duplicates, there should also not be
// ANYTHING".
//
// So: the bar says it while there is a bar to say it in, and it retires by
// itself; the log keeps it for anyone who asks afterwards. A message printed
// past the last frame is a message nobody asked for, over a shell prompt that
// is already back.
func (t *livelogTurn) report(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	slog.Warn("figaro session", "report", text)
	t.status.setNotice(text)
	if t.tr.active {
		t.tr.render()
	}
}

func (t *livelogTurn) transcriptSelect(delta int, extend bool) {
	t.tr.selectNode(delta, extend)
	t.tr.render()
}

// transcriptClickable reports whether a left click on this screen row would hit
// a node. The input loop asks BEFORE it acts so that a click on chrome does not
// cancel a search prompt the reader is still typing into: the mouse is the one
// input device that reports where it landed rather than what was aimed at.
func (t *livelogTurn) transcriptClickable(row int) bool {
	return t.tr.active && t.tr.clickable(row)
}

// transcriptClick selects the node on a screen row, or toggles its expansion
// when it is already the focus. Reports whether anything changed.
func (t *livelogTurn) transcriptClick(row int, extend bool) bool {
	if !t.tr.active {
		return false
	}
	// A panel is dismissed by any acting gesture, exactly as it is by any key the
	// panel does not itself bind (see transcript.dispatch). The click still
	// resolves against the frame the panel was drawn over, which is the frame the
	// user clicked: that is what makes frameRefs the right authority.
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

// leaveTranscript restores the normal screen (mouse off, alt-screen off) and
// flushes the tail of the conversation into native scrollback, so exiting the
// pager leaves the last turn behind as though it had been a normal inline
// command. Idempotent; a no-op when the pager isn't up.
func (t *livelogTurn) leaveTranscript() {
	if !t.tr.active {
		return
	}
	t.tr.leave()
	t.flushTail()
	// NOTHING IS SAID ON THE WAY OUT. Leaving the pager used to reprint every
	// report to the shell, which is how one hangup became two lines and a
	// disconnect became four. What the reader saw in the bar was the message;
	// the log has the rest.
	t.status.setNotice("")
}

// scrollbackTailRows is how many physical rows of conversation leaving the
// pager may put into native scrollback. The message-level bounds below (the
// freeze boundary, and lastTurnStart on a cold exit) are not bounds a reader
// can feel: ONE turn of tool output is routinely thousands of rows, and Ctrl-T
// dumped every one of them into the shell. A hundred rows is a screen or two -
// enough to see where you left off, and the rest is a `figaro show` away.
const scrollbackTailRows = 100

// flushTail (re)prints the un-frozen tail of the conversation to scrollback.
// Boundary: whatever incipit already froze inline stays put; only what
// streamed while the pager was up is emitted, and only its last
// scrollbackTailRows rows. If we entered the pager cold (nothing frozen
// inline, e.g. `figaro listen`), bound the dump to the last turn rather than
// replaying the whole history. Resume clears the partial live region the
// alt-screen restore left behind, prints the closed messages, and: if a
// message is still streaming: reopens a live region.
func (t *livelogTurn) flushTail() {
	v := t.client.View()

	// A turn reaches scrollback as SEVERAL messages, appendTurnSlices cuts it at
	// node boundaries and every slice carries the same LT with a rising From: so
	// both the boundary and the de-dup key on (LT, From). Keying on LT alone
	// collapsed a turn to its first slice, which is how a completed reply watched
	// in the pager never reached scrollback at all.
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

// lastTurnStart returns the LT of the most recent user message (the start of
// the last turn), or a best-effort fallback, so a cold pager exit records just
// the final turn rather than the entire conversation.
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

// transcriptScroll moves the pager viewport by delta lines (native wheel).
func (t *livelogTurn) transcriptScroll(delta int) { t.tr.scrollBy(delta) }

// transcriptMode is the keymap's view of the pager: which input mode a
// keystroke lands in.
func (t *livelogTurn) transcriptMode() keyMode { return t.tr.mode() }

// Transcript page fetches run off-lock; applying a page restores the viewport
// anchor and evicts the far edge of the bounded window.
func (t *livelogTurn) transcriptPageCursor() (transcriptPageRequest, bool) {
	return t.tr.pageCursor()
}
func (t *livelogTurn) transcriptApplyPage(req transcriptPageRequest, page historyPage) {
	t.tr.applyPage(req, page)
}

// setHistoryFetcher installs the reader Store.Ensure closes holes with. It is
// wired when the pager opens rather than at construction, for the same reason
// setQueuedFetch is: the input loop owns the RPC client.
func (t *livelogTurn) setHistoryFetcher(f aria.Fetcher) { t.client.SetFetcher(f) }

// fillGap closes a hole INSIDE the window. It runs on the prefetch worker, off
// the render lock: Client.Ensure takes the client's mutex only to ask what is
// missing and to fold what came back, so the pager keeps painting (the gap row
// included) while the read is outstanding.
func (t *livelogTurn) fillGap(ctx context.Context, g aria.Gap) error {
	return t.client.Ensure(ctx, g.From, g.To)
}

// transcriptFilled re-derives the window after a fill and repaints. The hole
// is gone from the store, so the line index simply stops emitting a sentinel
// for it.
func (t *livelogTurn) transcriptFilled() {
	t.tr.invalidateWindow()
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

// setQueuedFetch wires the transcript's async refresh callback (used when the
// queued-prompts panel opens). Wiring happens the first time the pager enters,
// not at construction, so the input loop: which owns the RPC client: can
// hand it in without a cyclic dependency at newLivelogTurn time.
func (t *livelogTurn) setQueuedFetch(fn func()) { t.tr.queuedFetch = fn }

// setTranscriptQueued updates the queued-prompts panel snapshot.
func (t *livelogTurn) setTranscriptQueued(prompts []queuedItem, errMsg string) {
	t.setQueued(prompts, errMsg)
}

// ariaView renders a block by reusing figaro's existing node renderers, so
// inline and transcript draw identically. One representation: livedoc.Node,
// and one dispatch: renderNode.
type ariaView struct {
	settings *renderSettings
	// gesture is true only on a surface that HAS a per-node expansion gesture
	//: the pager. It exists because Composer.Expanded == nil means "this
	// surface cannot un-collapse anything, so draw the fullest form", which is
	// right for OUTPUT (the incipit freezes to scrollback; `show` is a one-shot
	// dump) and wrong for ARGUMENTS, whose collapsed form is a live window the
	// reader is meant to watch. Without it the incipit asked for the whole
	// argument on every frame and the moving window never appeared.
	gesture bool
}

// pagerView returns v as the pager sees it: a surface where Enter means
// something, so per-node expansion may open arguments as well as output. Any
// other NodeView passes through unchanged.
func pagerView(v ldrender.NodeView) ldrender.NodeView {
	av, ok := v.(*ariaView)
	if !ok {
		return v
	}
	c := *av
	c.gesture = true
	return &c
}

// Render draws a node in its DEFAULT form for the live incipit.
func (v *ariaView) Render(n livedoc.Node, width, tick int) []string {
	return v.RenderExpanded(n, width, tick, true)
}

// RenderExpanded draws a node in its expanded or collapsed form. fullOutput is
// the transcript's per-node expansion state (t.expanded[ref]), and it now
// decides BOTH of a tool's collapsible parts: Enter on the selection opens the
// output and the arguments together, which is what "expand this" has always
// looked like it meant.
func (v *ariaView) RenderExpanded(n livedoc.Node, width, tick int, fullOutput bool) []string {
	verbose := v.settings != nil && v.settings.verbose
	// Expansion is a per-node GESTURE, and only the pager has one. A surface
	// without one draws the minimized form: clamped body, `… last N of M
	// lines` above it: rather than the fullest one.
	expand := verbose || (v.gesture && fullOutput)
	cap := nodeBashCapDefault
	if expand {
		cap = nodeOutputUnlimited
	}
	return renderNode(n, width, cap, uint64(tick), verbose, expand)
}

// openRule prints the session's opening rule through the inline renderer, which
// then knows the first message sits directly under it and needs no top margin
// of its own. See ldrender.Incipit.OpenRule.
func (t *livelogTurn) openRule() { t.in.OpenRule() }

// The queue, shown rather than echoed.
func (t *livelogTurn) setQueued(prompts []queuedItem, errMsg string) {
	t.queued, t.queuedErr = prompts, errMsg
	// THE SNAPSHOT LANDS FIRST. The pit builds its rows from the transcript's
	// copy, so telling the pit to refresh before handing it the new queue made
	// every refresh one poll stale: a queue that filled while the pit was open
	// kept drawing "(none)" until the pit was closed and opened again, which
	// is the same symptom as a cursor that never arrives and was hiding behind
	// it.
	t.tr.queued = t.queued
	t.tr.queuedRows = t.queuedRows()
	// The panel opens itself when there is something to show and closes when
	// there is not. Auto-close is skipped once the user has opened it by hand,
	// so a deliberate `Q` is not yanked away the moment the queue drains.
	if len(prompts) > 0 || errMsg != "" {
		t.tr.showQueuedAuto(true)
	} else {
		t.tr.showQueuedAuto(false)
	}
}

// queuedRows is the ONE rendering of the queue, so the inline trailer and the
// pager's panel cannot drift. Dim, clipped, bounded: a queue deep enough to
// fill the screen is still just a hint that work is stacked up.
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
			fmt.Sprintf("   %d. %s", i+1, firstLineTrim(p)), w)))
	}
	return rows
}

// queuedRowsMax bounds the inline trailer. The live region may not exceed the
// viewport (see Incipit), and a long queue must not be the thing that pushes
// the reply off the top.
const queuedRowsMax = 5

// invalidateTranscriptWindow re-derives the pager's window. Older messages that
// land AFTER the window was built (the catch-up read a promotion owes) are in
// the store but not yet in the index; this is what puts them within reach of a
// scroll.
func (t *livelogTurn) invalidateTranscriptWindow() { t.tr.invalidateWindow() }

// retarget points this renderer at a DIFFERENT aria: the transcript's subject
// changes, and everything aria-scoped is rebuilt around the new one.
//
// It is a true reload, deliberately: a fresh client, an empty store, a cold
// window. The cheaper thing -- keeping the turns the two arias SHARE, which for
// a fork is nearly all of them -- needs the wire to say where two arias diverge,
// and it does not yet. See plans/transcript-subject.md §3; this function is the
// seam that work optimises, and it is the only one.
//
// Called with the render lock held.
func (t *livelogTurn) retarget(figaroID string, status *sessionStatus) {
	t.client = aria.NewClient()
	t.status = status
	t.wireClient()

	// Everything below is state ABOUT the old aria, and every field of it that
	// survives is a way for the new transcript to render the old conversation.
	t.open = aria.Message{}
	t.pending = nil
	t.finished = false
	t.thinkingOpen = false
	t.lastFrozen = sliceCursor{}
	t.pagerClosed = nil
	t.queued, t.queuedErr = nil, ""
	t.hold, t.held = false, nil
	t.seeded, t.seedExtents, t.seedMore = nil, nil, false

	t.tr.retarget(t.client, figaroID, status)
}

// setCommandRunner wires the ':' box's non-coordinate half to whoever owns the
// RPC clients, for the same reason setQueuedFetch and setHistoryFetcher are:
// the renderer must not know how to reach a daemon.
func (t *livelogTurn) setCommandRunner(fn func(string)) { t.tr.command = fn }

// setTranscriptCmdOut shows a command's captured output in the footer panel.
func (t *livelogTurn) setTranscriptCmdOut(title string, rows []string) {
	t.tr.setCmdOut(title, rows)
}

// setCommandCompleter wires Tab in the ':' box to the router's completion.
func (t *livelogTurn) setCommandCompleter(fn func(string) []string) { t.tr.completer = fn }

// queuedItem is one queued message as the UI holds it: the text to show and the
// ID a verb addresses. See livelogTurn.queued for why the id survives.
type queuedItem struct {
	id   uint64
	text string
}
