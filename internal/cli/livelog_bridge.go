package cli

import (
	"io"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// livelogTurn renders the aria-read wire. By default it uses the inline-seal
// renderer (closed messages seal to scrollback once; the open message is the one
// live region). Ctrl-T toggles a full-screen transcript pager (see transcript.go)
// that shares the same aria.Client model, so both render the same conversation;
// only the active view paints. Messages that close while the pager is up are
// queued and flushed to the inline scrollback on exit, so nothing is lost.
type livelogTurn struct {
	in     *ldrender.Inline
	term   *ldrender.ANSITerminal
	client *aria.Client
	view   *ariaView
	tr     *transcript

	openLT   int
	openRole string
	open     []livedoc.Node

	pendingSeals []aria.Message // closed while in the pager; flushed inline on exit
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, bookend, rule func() string) *livelogTurn {
	view := &ariaView{settings: settings}
	term := ldrender.NewANSITerminal(out, w, h)
	in := ldrender.NewInline(term, view)
	in.Bookend = bookend
	in.Rule = rule
	in.Header = messageHeader
	t := &livelogTurn{in: in, term: term, client: aria.NewClient(), view: view}
	t.tr = newTranscript(out, w, h, view, t.client)
	t.client.OnClosed = func(m aria.Message) {
		if t.tr.active {
			t.pendingSeals = append(t.pendingSeals, m)
			if m.LT == t.openLT {
				t.openLT, t.open = 0, nil // closed: don't re-open it on pager exit
			}
			t.tr.render()
		} else {
			t.in.Seal(m)
		}
	}
	t.client.OnLive = func(lt int, role string, nodes []livedoc.Node) {
		t.openLT, t.openRole, t.open = lt, role, nodes
		if t.tr.active {
			t.tr.render()
		} else {
			t.in.Open(lt, role, nodes)
		}
	}
	return t
}

func (t *livelogTurn) apply(r aria.AriaRead)  { t.client.Apply(r) }
func (t *livelogTurn) setDesync(fn func(int)) { t.client.OnDesync = fn }
func (t *livelogTurn) transcriptActive() bool { return t.tr.active }

// abandon closes a live region without a normal Seal: paint a labeled
// dim rule across the boundary so what follows isn't glued to the orphaned
// output. reason is the short label (e.g. "disconnected — turn continues").
func (t *livelogTurn) abandon(reason string) { t.in.AbandonOpen(abandonRule(reason)) }

func (t *livelogTurn) tick() {
	// Only a running tool's spinner needs the periodic repaint. With nothing
	// animating the tick would recompose + diff the whole open message every
	// frame for a no-op paint — pure waste. Content changes still repaint via
	// the OnLive/OnClosed hooks, so gating here is invisible. (The transcript
	// branch already did this; the inline branch didn't.)
	if !t.client.OpenAnimating() {
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

// enterTranscript switches to the full-screen pager (the caller has already
// caught the model up via figaro.read so it shows full history).
func (t *livelogTurn) enterTranscript() { t.tr.enter() }

// transcriptKey routes a key to the pager. On exit it restores the normal
// screen, flushes messages that closed while paging to the inline scrollback,
// and resumes inline rendering of the open message. Returns true on exit.
func (t *livelogTurn) transcriptKey(b byte) (exited bool) {
	if !t.tr.key(b) {
		return false
	}
	t.tr.leave()
	t.in.Resume(t.pendingSeals, t.openLT, t.openRole, t.open)
	t.pendingSeals = nil
	return true
}

// ariaView renders a block by reusing figaro's existing node renderers, so
// inline and transcript draw identically. One representation: livedoc.Node.
type ariaView struct{ settings *renderSettings }

func (v *ariaView) Render(n livedoc.Node, width, tick int) []string {
	switch n.Type {
	case livedoc.NodeTool:
		return renderToolNode(n, width, nodeBashCapDefault, uint64(tick), v.settings != nil && v.settings.verbose)
	case livedoc.NodeThinking:
		return renderThinkingNode(n, width)
	case livedoc.NodeSteering:
		return renderSteeringNode(n, width)
	default:
		return renderProseNode(n, width)
	}
}
