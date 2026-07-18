package cli

import (
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// livelogTurn renders the aria-read wire. By default it uses the incipit-seal
// renderer (closed messages seal to scrollback once; the open message is the one
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

	openLT   int
	openRole string
	open     []livedoc.Node
	pending  *aria.Message
	finished bool

	// lastSealedLT is the highest LT incipit has committed to native scrollback
	// inline (via Seal). It marks the flush boundary: on leaving the pager,
	// everything past it is (re)printed to scrollback, so the turn you watched in
	// the pager is left behind like a normal command. 0 means nothing was sealed
	// inline (we entered the pager cold, e.g. `figaro listen`).
	lastSealedLT int
	pagerClosed  []aria.Message
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, figaroID string, startedAt time.Time, status *sessionStatus, bookend, rule func() string) *livelogTurn {
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
		if t.tr.active {
			if t.lastSealedLT != 0 {
				t.pagerClosed = append(t.pagerClosed, m)
			}
			t.tr.render() // transcript renders from the shared client model
		} else if m.Role == "assistant" {
			t.pending = &m
			if t.finished {
				t.sealPending()
			}
		} else {
			t.in.Seal(m) // incipit: seal to native scrollback
			if m.LT > t.lastSealedLT {
				t.lastSealedLT = m.LT
			}
		}
	}
	t.client.OnLive = func(lt int, role string, nodes []livedoc.Node) {
		newOpen := lt != t.openLT
		t.openLT, t.openRole, t.open = lt, role, nodes
		if role == "assistant" {
			if newOpen {
				t.finished = false
			}
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
			t.in.Open(lt, role, nodes)
		}
	}
	return t
}

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

func (t *livelogTurn) apply(r aria.AriaRead)  { t.client.Apply(r) }
func (t *livelogTurn) setDesync(fn func(int)) { t.client.OnDesync = fn }
func (t *livelogTurn) transcriptActive() bool { return t.tr.active }

// abandon closes a live region without a normal Seal: paint a labeled
// dim rule across the boundary so what follows isn't glued to the orphaned
// output. reason is the short label (e.g. "disconnected — turn continues").
//
// If the pager is up, restore the screen and flush the tail to scrollback
// FIRST (so the labeled rule lands below the recovered turn, not on the
// about-to-be-torn-down alt screen), then draw the rule.
func (t *livelogTurn) abandon(reason string) {
	t.status.finishTurn(reason)
	t.pending = nil
	t.leaveTranscript()
	t.in.AbandonOpen(abandonRule(reason))
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

func (t *livelogTurn) resize(w, h int) {
	t.term.SetSize(w, h)
	if t.tr.active {
		t.tr.resize(w, h)
	} else {
		t.in.Resize(t.open)
	}
}

// render repaints the active view (e.g. after a verbosity toggle).
func (t *livelogTurn) render() {
	if t.tr.active {
		t.tr.render()
	} else if t.openLT != 0 {
		t.in.Open(t.openLT, t.openRole, t.open)
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
	t.sealPending()
	if !hadPending && t.openLT != 0 && t.openRole == "assistant" {
		t.in.Open(t.openLT, t.openRole, t.open)
		if strings.HasPrefix(strings.ToLower(reason), "error:") {
			t.in.AbandonOpen("")
		}
	}
}

func (t *livelogTurn) sealPending() {
	if t.pending != nil {
		t.in.Open(t.pending.LT, t.pending.Role, t.pending.Nodes)
		t.in.Seal(*t.pending)
		if t.pending.LT > t.lastSealedLT {
			t.lastSealedLT = t.pending.LT
		}
		t.pending = nil
	}
}

// enterTranscript switches to the full-screen pager (the caller has already
// caught the model up via figaro.read so it shows full history).
func (t *livelogTurn) enterTranscript() { t.tr.enter() }

// transcriptKey routes a navigation/search key to the locked transcript.
// Transcript never self-exits; leaving is Ctrl-D/Ctrl-C at the input loop.
func (t *livelogTurn) transcriptKey(b byte) { t.tr.key(b) }

func (t *livelogTurn) invalidateTranscriptRows() { t.tr.invalidateRows() }

func (t *livelogTurn) transcriptSelect(delta int, extend bool) {
	t.tr.selectNode(delta, extend)
	t.tr.render()
}

func (t *livelogTurn) transcriptHasSelection() bool { return t.tr.hasSelection() }

func (t *livelogTurn) transcriptSelectedText() (string, bool) { return t.tr.selectedText() }

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

// flushTail (re)prints the un-sealed tail of the conversation to scrollback.
// Boundary: whatever incipit already sealed inline stays put; only what
// streamed while the pager was up is emitted. If we entered the pager cold
// (nothing sealed inline, e.g. `figaro listen`), bound the dump to the last
// turn rather than replaying the whole history. Resume clears the partial live
// region the alt-screen restore left behind, prints the closed messages in
// full, and — if a message is still streaming — reopens a live region.
func (t *livelogTurn) flushTail() {
	v := t.client.View()
	from := t.lastSealedLT + 1
	if t.lastSealedLT == 0 {
		from = lastTurnStartLT(v)
	}
	var closed []aria.Message
	seen := make(map[int]bool)
	for _, m := range t.pagerClosed {
		if m.LT >= from {
			closed = append(closed, m)
			seen[m.LT] = true
		}
	}
	for _, m := range v.Closed {
		if m.LT >= from && !seen[m.LT] {
			closed = append(closed, m)
		}
	}
	t.pagerClosed = nil
	openLT, openRole := 0, ""
	var open []livedoc.Node
	if v.Open != nil && v.Open.LT >= from {
		openLT, openRole, open = v.Open.LT, v.Open.Role, v.Open.Nodes
	}
	if len(closed) == 0 && openLT == 0 {
		return
	}
	sort.SliceStable(closed, func(i, j int) bool { return closed[i].LT < closed[j].LT })
	t.in.Resume(closed, openLT, openRole, open)
	if len(closed) > 0 {
		t.lastSealedLT = closed[len(closed)-1].LT
	}
}

// lastTurnStartLT returns the LT of the most recent user message (the start of
// the last turn), or a best-effort fallback, so a cold pager exit records just
// the final turn rather than the entire conversation.
func lastTurnStartLT(v aria.View) int {
	for k := len(v.Closed) - 1; k >= 0; k-- {
		if v.Closed[k].Role == "user" {
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

// transcriptSearching reports whether the pager is in its search prompt, so the
// input loop routes typeable keys (like 'y') to the query instead of acting.
func (t *livelogTurn) transcriptSearching() bool { return t.tr.active && t.tr.inSearch }

// Transcript page fetches run off-lock; applying a page restores the viewport
// anchor and evicts the far edge of the bounded window.
func (t *livelogTurn) transcriptPageCursor() (transcriptPageRequest, bool) {
	return t.tr.pageCursor()
}
func (t *livelogTurn) transcriptApplyPage(req transcriptPageRequest, r aria.AriaRead) {
	t.tr.applyPage(req, r)
}
func (t *livelogTurn) transcriptSearchingHistory() bool { return t.tr.searchingHistory() }
func (t *livelogTurn) transcriptPageFailed() {
	t.tr.finishSearch(false)
	t.tr.checkOlder, t.tr.checkNewer = false, false
	t.tr.render()
}

// ariaView renders a block by reusing figaro's existing node renderers, so
// inline and transcript draw identically. One representation: livedoc.Node.
type ariaView struct{ settings *renderSettings }

func (v *ariaView) Render(n livedoc.Node, width, tick int) []string {
	return v.RenderExpanded(n, width, tick, false)
}

func (v *ariaView) RenderExpanded(n livedoc.Node, width, tick int, fullOutput bool) []string {
	switch n.Type {
	case livedoc.NodeTool:
		bashCap := nodeBashCapDefault
		if fullOutput {
			bashCap = nodeOutputUnlimited
		}
		return renderToolNode(n, width, bashCap, uint64(tick), v.settings != nil && v.settings.verbose)
	case livedoc.NodeThinking:
		return renderThinkingNode(n, width)
	case livedoc.NodeSteering:
		return renderSteeringNode(n, width)
	default:
		return renderProseNode(n, width)
	}
}
