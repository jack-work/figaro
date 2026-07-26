package cli

import (
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
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

	openLT       int
	openFrom     uint64
	openRole     string
	open         []livedoc.Node
	pending      *aria.Message
	finished     bool
	thinkingOpen bool // an OpenThinking placeholder is live and not yet adopted
	pace         framePacer

	// lastFrozen is the highest SLICE incipit has committed to native scrollback
	// inline (via Freeze). It marks the flush boundary: on leaving the pager,
	// everything past it is (re)printed to scrollback, so the turn you watched in
	// the pager is left behind like a normal command. A zero lt means nothing was
	// frozen inline (we entered the pager cold, e.g. `figaro listen`).
	//
	// It is a (turn, node-offset) PAIR, not a bare turn id. appendTurnSlices cuts
	// one turn into several messages that all carry the same LT with a rising
	// From, so a turn-granular boundary either replays a slice already on screen
	// or — the bug this replaced — skips every slice after the first.
	lastFrozen  sliceCursor
	pagerClosed []aria.Message
}

// sliceCursor addresses one pager unit: the turn and the node offset within it.
type sliceCursor struct {
	lt   int
	from uint64
}

func cursorOf(m aria.Message) sliceCursor { return sliceCursor{lt: m.LT, from: m.From} }

// after reports whether c comes strictly after o in reading order.
func (c sliceCursor) after(o sliceCursor) bool {
	if c.lt != o.lt {
		return c.lt > o.lt
	}
	return c.from > o.from
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, figaroID string, startedAt time.Time, status *sessionStatus, bookend func() []string, rule func() string) *livelogTurn {
	view := &ariaView{settings: settings}
	term := ldrender.NewANSITerminal(out, w, h)
	in := ldrender.NewIncipit(term, view)
	in.Bookend = bookend
	in.Rule = rule
	in.Header = messageHeader
	t := &livelogTurn{in: in, term: term, client: aria.NewClient(), view: view, status: status}
	t.client.SetClosedLimit(transcriptTailLimit)
	t.tr = newTranscript(out, w, h, view, t.client, figaroID, startedAt)
	if status != nil {
		t.tr.status = status
		t.client.OnMetrics = status.update
	}
	t.client.OnClosed = func(m aria.Message) {
		t.tr.observeCommitted(m)
		if t.tr.active {
			if t.lastFrozen.lt != 0 {
				t.pagerClosed = append(t.pagerClosed, m)
			}
			t.tr.render() // transcript renders from the shared client model
		} else if m.Role == livedoc.RoleOutput {
			t.pending = &m
			if t.finished {
				t.freezePending()
			}
		} else {
			t.in.Freeze(m) // incipit: freeze to native scrollback
			if c := cursorOf(m); c.after(t.lastFrozen) {
				t.lastFrozen = c
			}
		}
	}
	t.client.OnLive = func(lt int, from uint64, role string, nodes []livedoc.Node) {
		newOpen := lt != t.openLT
		t.openLT, t.openFrom, t.openRole, t.open = lt, from, role, nodes
		if role == livedoc.RoleOutput {
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
		if !t.tr.active && t.openOverflows(nodes) {
			t.tr.enter()
		}
		if t.tr.active {
			t.tr.render()
		} else {
			t.in.Open(lt, from, role, nodes)
		}
	}
	return t
}

// transcriptFrameInterval is the pager's frame-rate ceiling. Live aria frames
// arrive far faster than a terminal can usefully show them — a streaming tool
// can push dozens of deltas per second, each of which used to trigger a full
// repaint of the pager. 120 fps is above anything a human resolves and well
// above any terminal's own refresh, so nothing is lost by refusing to draw
// more often than this.
const transcriptFrameInterval = time.Second / 120

// framePacer is the transcript's frame-rate gate. It answers "may I paint
// now?" and, when the answer is no, owes a trailing flush so the settled state
// always reaches the screen — dropping the LAST frame of a burst is the one
// failure mode a rate limiter must not have.
//
// Every field is guarded by the caller's render mutex: allow() and painted()
// run under it (all render paths hold it), and the trailing timer takes it
// before flushing. No second lock, and therefore no lock ordering to get
// wrong.
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

// armThinking pins the footer the instant a submit is accepted — before the
// prompt has round-tripped, before the model's first token. The footer is a
// permanent fixture of the view, so it must not wait on the stream.
//
// It used to arm a flag consumed when the prompt froze, which required the
// prompt to arrive as its own CLOSED message. Once the prompt merged into the
// turn it no longer reliably does, and the footer went missing until the first
// token — so paint it here instead of waiting to be told.
//
// No-op in the pager (it renders the footer itself).
func (t *livelogTurn) armThinking() {
	if t.tr.active {
		return
	}
	t.thinkingOpen = true
	t.status.beginTurn()
	t.in.OpenThinking(livedoc.RoleOutput)
}

func (t *livelogTurn) apply(r aria.Page)      { t.client.Apply(r) }
func (t *livelogTurn) setDesync(fn func(int)) { t.client.OnDesync = fn }
func (t *livelogTurn) transcriptActive() bool { return t.tr.active }

// abandon closes a live region without a normal Freeze: paint a labeled
// dim rule across the boundary so what follows isn't glued to the orphaned
// output. reason is the short label (e.g. "disconnected — turn continues").
//
// If the pager is up, restore the screen and flush the tail to scrollback
// FIRST (so the labeled rule lands below the recovered turn, not on the
// about-to-be-torn-down alt screen), then draw the rule.
//
// A turn that ALREADY FINISHED is not being abandoned — the pager was merely
// closed after the fact. Flushing the completed tail and keeping the real
// outcome is the whole job there; overwriting the status with "turn continues"
// and dropping t.pending is how a reply the user watched arrive was lost.
// Reports whether it actually abandoned anything, so the caller can skip the
// "follow: figaro listen" hint for a turn that is already over.
func (t *livelogTurn) abandon(reason string) bool {
	if t.finished {
		t.leaveTranscript()
		t.freezePending()
		return false
	}
	t.status.finishTurn(reason)
	t.pending = nil
	t.leaveTranscript()
	t.in.AbandonOpen(abandonRule(reason))
	return true
}

func (t *livelogTurn) tick() {
	// Only a running tool's spinner needs the periodic repaint. With nothing
	// animating the tick would recompose + diff the whole open message every
	// frame for a no-op paint — pure waste. Content changes still repaint via
	// the OnLive/OnClosed hooks, so gating here is invisible. (The transcript
	// branch already did this; the inline branch didn't.)
	thinking := t.status.advance()
	if !t.client.OpenAnimating() && !thinking {
		return
	}
	if t.tr.active {
		t.tr.tick++
		t.tr.render()
	} else {
		t.in.Tick(t.open)
	}
}

// resize repaints for the new geometry, escaping to the pager when the shrink
// is destructive. A viewport shorter than the live region is the one case
// inline drawing cannot fix — the terminal scrolls those rows into native
// scrollback before our code runs, so they are unreachable for in-place
// repaint. The pager has no live region to lose.
//
// This is the complement of openOverflows, which catches the same hazard from
// the other direction (content growing past a fixed viewport), and it promotes
// the same cheap way: tr.enter() renders from the client model already in
// hand. Doing a catch-up read here instead would stall the resize handler for
// seconds while incipit kept painting into a viewport that no longer fits it.
//
// Gated on minPagerHeight so a pane too small for the pager's own chrome does
// not thrash into it.
func (t *livelogTurn) resize(w, h int) {
	t.term.SetSize(w, h)
	if !t.tr.active && h >= minPagerHeight && t.in.LiveHeight() > h {
		t.tr.enter()
	}
	if t.tr.active {
		t.tr.resize(w, h)
		return
	}
	t.in.Resize(t.open)
}

// render repaints the active view (e.g. after a verbosity toggle).
func (t *livelogTurn) render() {
	if t.tr.active {
		t.tr.render()
	} else if t.openLT != 0 {
		t.in.Open(t.openLT, t.openFrom, t.openRole, t.open)
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
	} else if !hadPending && t.openLT != 0 && t.openRole == livedoc.RoleOutput {
		t.in.Open(t.openLT, t.openFrom, t.openRole, t.open)
		if strings.HasPrefix(strings.ToLower(reason), "error:") {
			t.in.AbandonOpen("")
		}
	}
}

func (t *livelogTurn) freezePending() {
	if t.pending != nil {
		t.in.Open(t.pending.LT, t.pending.From, t.pending.Role, t.pending.Nodes)
		t.in.Freeze(*t.pending)
		if c := cursorOf(*t.pending); c.after(t.lastFrozen) {
			t.lastFrozen = c
		}
		t.pending = nil
	}
}

// enterTranscript switches to the full-screen pager (the caller has already
// caught the model up via figaro.read so it shows full history).
func (t *livelogTurn) enterTranscript() { t.tr.enter() }

// inTranscript reports whether the pager is up. Read under the render lock.
func (t *livelogTurn) inTranscript() bool { return t.tr.active }

// transcriptDispatch routes one decoded keystroke to the locked transcript.
// One path in, whatever the key's encoding was: see (*transcript).dispatch.
func (t *livelogTurn) transcriptDispatch(ev keyEvent) { t.tr.dispatch(ev) }

func (t *livelogTurn) invalidateTranscriptRows() { t.tr.invalidateRows() }

func (t *livelogTurn) transcriptSelect(delta int, extend bool) {
	t.tr.selectNode(delta, extend)
	t.tr.render()
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
}

// flushTail (re)prints the un-frozen tail of the conversation to scrollback.
// Boundary: whatever incipit already froze inline stays put; only what
// streamed while the pager was up is emitted. If we entered the pager cold
// (nothing frozen inline, e.g. `figaro listen`), bound the dump to the last
// turn rather than replaying the whole history. Resume clears the partial live
// region the alt-screen restore left behind, prints the closed messages in
// full, and — if a message is still streaming — reopens a live region.
func (t *livelogTurn) flushTail() {
	v := t.client.View()

	// A turn reaches scrollback as SEVERAL messages — appendTurnSlices cuts it at
	// node boundaries and every slice carries the same LT with a rising From — so
	// both the boundary and the de-dup key on (LT, From). Keying on LT alone
	// collapsed a turn to its first slice, which is how a completed reply watched
	// in the pager never reached scrollback at all.
	cold := t.lastFrozen.lt == 0
	coldFrom := 0
	if cold {
		coldFrom = lastTurnStartLT(v)
	}
	want := func(m aria.Message) bool {
		if cold {
			return m.LT >= coldFrom
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
	openLT, openRole := 0, ""
	var open []livedoc.Node
	if v.Open != nil && (cold && v.Open.LT >= coldFrom || !cold && v.Open.LT >= t.lastFrozen.lt) {
		openLT, openRole, open = v.Open.LT, v.Open.Role, v.Open.Nodes
	}
	if len(closed) == 0 && openLT == 0 {
		return
	}
	sort.SliceStable(closed, func(i, j int) bool { return cursorOf(closed[j]).after(cursorOf(closed[i])) })
	t.in.Resume(closed, openLT, t.openFrom, openRole, open)
	if len(closed) > 0 {
		t.lastFrozen = cursorOf(closed[len(closed)-1])
	}
}

// lastTurnStartLT returns the LT of the most recent user message (the start of
// the last turn), or a best-effort fallback, so a cold pager exit records just
// the final turn rather than the entire conversation.
func lastTurnStartLT(v aria.View) int {
	for k := len(v.Closed) - 1; k >= 0; k-- {
		if v.Closed[k].Role == livedoc.RoleInput {
			return v.Closed[k].LT
		}
	}
	if v.Open != nil {
		return v.Open.LT
	}
	if n := len(v.Closed); n > 0 {
		return v.Closed[n-1].LT
	}
	return 0
}

// transcriptScroll moves the pager viewport by delta lines (native wheel).
func (t *livelogTurn) transcriptScroll(delta int) { t.tr.scrollBy(delta) }

// transcriptSearching reports whether the pager is in its search prompt. The
// input loop no longer asks — the search box is a keymap mode now (modeSearch,
// see transcriptMode) — but it remains the plain question to ask about state.
func (t *livelogTurn) transcriptSearching() bool { return t.tr.active && t.tr.inSearch }

// transcriptMode is the keymap's view of the pager: which of the four input
// modes a keystroke lands in. The composer is asked FIRST and sits above the
// pager, because it is the one text box that must work inline as well — a
// short turn never promotes, and the user still wants to steer it.
func (t *livelogTurn) transcriptMode() keyMode {
	if t.status.composingNow() {
		return modeCompose
	}
	return t.tr.mode()
}

// Transcript page fetches run off-lock; applying a page restores the viewport
// anchor and evicts the far edge of the bounded window.
func (t *livelogTurn) transcriptPageCursor() (transcriptPageRequest, bool) {
	return t.tr.pageCursor()
}
func (t *livelogTurn) transcriptApplyPage(req transcriptPageRequest, messages []aria.Message) {
	t.tr.applyPage(req, messages)
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
	t.tr.checkOlder, t.tr.checkNewer = false, false
	t.tr.render()
}

// setQueuedFetch wires the transcript's async refresh callback (used when the
// queued-prompts panel opens). Wiring happens the first time the pager enters,
// not at construction, so the input loop — which owns the RPC client — can
// hand it in without a cyclic dependency at newLivelogTurn time.
func (t *livelogTurn) setQueuedFetch(fn func()) { t.tr.queuedFetch = fn }

// setTranscriptQueued updates the queued-prompts panel snapshot.
func (t *livelogTurn) setTranscriptQueued(prompts []string, errMsg string) {
	t.tr.setQueued(prompts, errMsg)
}

// ariaView renders a block by reusing figaro's existing node renderers, so
// inline and transcript draw identically. One representation: livedoc.Node,
// and one dispatch: renderNode.
type ariaView struct{ settings *renderSettings }

func (v *ariaView) Render(n livedoc.Node, width, tick int) []string {
	return v.RenderExpanded(n, width, tick, false)
}

func (v *ariaView) RenderExpanded(n livedoc.Node, width, tick int, fullOutput bool) []string {
	bashCap := nodeBashCapDefault
	if fullOutput {
		bashCap = nodeOutputUnlimited
	}
	return renderNode(n, width, bashCap, uint64(tick), v.settings != nil && v.settings.verbose)
}
