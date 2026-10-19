package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/mark"

	"github.com/jack-work/figaro/sdk"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

const spinnerFPS = 11

// recentCursor is a beyond-the-end turn id: ReadBefore from it returns the newest page.
const recentCursor = 1 << 60

const (
	recentContextMessages = 4
	recentContextTimeout  = 300 * time.Millisecond
)

const (
	autowrapOff = "\x1b[?7l"
	autowrapOn  = "\x1b[?7h"
	cursorHide  = "\x1b[?25l"
	cursorShow  = "\x1b[?25h"
)

func mustPromptFigaro(ctx context.Context, ep sdk.Addr, figaroID, prompt string, loaded *config.Loaded, set renderSettings, d dressing) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var rec *tape.Writer
	if set.record != "" {
		var terr error
		rec, terr = tape.Create(set.record, tape.Header{
			Aria:    figaroID,
			Cols:    term.Width(),
			Rows:    term.Height(),
			Term:    os.Getenv("TERM"),
			Binary:  boot.BuildRevision(),
			Command: strings.Join(os.Args, " "),
		})
		if terr != nil {
			die("record: %s", terr)
		}
		defer func() {
			// To the log: the shell prompt is already back.
			if cerr := rec.Close(); cerr != nil {
				slog.Error("tape close", "err", cerr)
			}
		}()
	}

	runSession(ctx, cancel, sessionOpts{
		figaroID: figaroID, ep: ep, loaded: loaded, set: set,
		prompt: prompt, dress: d, tape: rec, ownsSubject: true,
	})
}

// An interrupted turn exits 130; Ctrl-C with nothing running is 0.
func interruptExit(wasRunning bool) int {
	if wasRunning {
		return exitInterrupted
	}
	return 0
}

// interactiveInput is the input loop shared by send and listen; the keymap owns
// every key from the keyEvent on.
type interactiveInput struct {
	tc   term.Client
	lt   *livelogTurn
	fcli transcriptReadClient
	// subject is the connection fcli names, held concretely so a switch can
	// close it and `:send` can Qua on it.
	subject      *sdk.Aria
	acli         *sdk.Angelus
	loaded       *config.Loaded
	tap          transport.Tap
	seed         seedPlan
	parked       map[string]*parkedSubject
	parkOrder    []string // oldest first
	lineageEpoch uint64
	ownsSubject  bool   // a switch may only close a connection this loop dialled
	subjectGen   uint64 // fences the old connection's handlers off the renderer the instant a switch begins
	subjectDead  chan struct{}
	jumps        ariaJumplist         // guarded by in.mu
	queueEpoch   string               // the daemon refuses a queue mutation without the epoch it was read at
	queueDebut   map[uint64]time.Time // guarded by in.mu
	debutTimer   *time.Timer
	intrinsics   *intrinsicMirrors
	board        *formMirror // the subject's form; guarded by in.mu, parked with the subject
	models       *modelsFeed
	interrupting bool         // a second Ctrl-C leaves at once: a daemon that will not answer must not trap the user
	hangup       hangupClient // nil leaves H inert
	// The send half. ownTurn closes when a frame carries our question, noTurn
	// when the agent errors instead; Qua's `active` is sampled before the
	// prompt is queued and can race, the event cannot.
	prompt       string
	dress        dressing
	sendCursor   int // set by Qua; a turn.done before it is not ours
	doneCh       chan struct{}
	turnFailed   bool // written on the notify pump under mu, read after the session's select returns
	ownTurn      chan struct{}
	noTurn       chan struct{}
	ownOnce      sync.Once
	noOnce       sync.Once
	watchInquiry bool
	startInline  bool
	mu           *renderLock
	set          *renderSettings
	figaroID     string
	cancel       context.CancelFunc
	disconnectCh chan struct{}
	copyCancel   context.CancelFunc
	copyGen      uint64
	copyPlan     selectionCopyPlan
	copyFailed   bool
	copyFailedLo selectionPoint
	copyFailedHi selectionPoint
	searchCancel context.CancelFunc
	searchGen    uint64
	searchQuery  string
	searchDone   chan struct{}
	pageInFlight bool          // one background history fetch at a time
	pageDone     chan struct{} // closed when the worker finishes; tests synchronize on it
	// pageWanted defers the paging check to the end of the input chunk: it
	// reads a viewport offset render clamps. Input goroutine only.
	pageWanted bool
	caughtUp   bool // guarded by mu; a failed read clears it
	// gate parks the read loop while an external program owns the terminal;
	// nil outside a raw TTY session.
	gate        inputGate
	suspendTerm func()
	resumeTerm  func()
	lastNL      byte // Windows conhost emits CR+LF for one Enter; the pair is coalesced
}

type hangupClient interface {
	Hangup(context.Context, rpc.QueueDisposition) (*rpc.InterruptResponse, error)
}

type transcriptReadClient interface {
	Read(context.Context, aria.Anchor, int) (aria.Page, error)
	ReadBefore(context.Context, aria.Anchor, int) (aria.Page, error)
	Queued(context.Context) (*rpc.QueuedResponse, error)
}

// tailReader is optional: without it a switch re-reads the prefix, which is slower
// and never wrong.
type tailReader interface {
	ReadBeforeFloor(ctx context.Context, at, floor aria.Anchor, budget int) (aria.Page, error)
}

func (in *interactiveInput) enterTranscript() { in.enterPager(true) }

// enterFormPager reads no conversation: a reader who came for the board may not be
// permitted to read it.
func (in *interactiveInput) enterFormPager() { in.enterPager(false) }

func (in *interactiveInput) enterPager(history bool) {
	in.mu.Lock()
	already := in.lt.transcriptActive()
	seeded := in.lt.hasSeed()
	if !history {
		// Claimed, so the automatic catch-up does not fire behind the pit.
		in.caughtUp = true
	}
	in.mu.Unlock()
	if already {
		return
	}
	if !history {
		in.mu.Lock()
		in.lt.enterTranscript()
		in.lt.setQueuedFetch(in.refreshQueued)
		in.mu.Unlock()
		return
	}
	if seeded {
		in.mu.Lock()
		in.caughtUp = true
		in.lt.enterTranscript()
		in.lt.setQueuedFetch(in.refreshQueued)
		in.lt.setHistoryFetcher(in.historyFetcher())
		in.mu.Unlock()
		return
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	read := mark.Span("hop.read", "kind", "enter")
	r, rerr := in.fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(transcriptPageSize))
	read("parts", len(r.Parts), "err", rerr != nil)
	rcancel()
	in.mu.Lock()
	// Claimed before the pager opens, or enterPager fires the catch-up hook
	// for a read just done.
	in.caughtUp = rerr == nil
	in.lt.enterTranscript()
	in.lt.setQueuedFetch(in.refreshQueued)
	in.lt.setHistoryFetcher(in.historyFetcher())
	if rerr == nil {
		in.lt.apply(r)
		in.lt.setMoreBefore(r.More.Before)
	}
	in.mu.Unlock()
}

func (in *interactiveInput) pagerCatchUp() {
	if in.caughtUp {
		return
	}
	in.caughtUp = true
	go in.readHistoryIntoPager()
}

// reseedIfEmpty retries a failed catch-up from the pager's clock.
func (in *interactiveInput) reseedIfEmpty() {
	in.mu.Lock()
	owed := in.lt.transcriptActive() && !in.caughtUp && in.fcli != nil
	if owed {
		in.caughtUp = true
	}
	in.mu.Unlock()
	if owed {
		go in.readHistoryIntoPager()
	}
}

func (in *interactiveInput) readHistoryIntoPager() {
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	read := mark.Span("hop.read", "kind", "catchup")
	r, rerr := in.fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(transcriptPageSize))
	read("parts", len(r.Parts), "err", rerr != nil)
	rcancel()
	in.mu.Lock()
	defer in.mu.Unlock()
	if rerr != nil {
		in.caughtUp = false
		return
	}
	// Applying is safe only because the pager is up: inline, OnClosed would
	// freeze this to scrollback.
	in.lt.apply(r)
	in.lt.setMoreBefore(r.More.Before)
	in.lt.invalidateTranscriptWindow()
	in.lt.render()
}

// pageTranscript never blocks: one fetch runs at a time and chains into the next
// while the pager still wants one.
func (in *interactiveInput) pageTranscript() {
	in.mu.Lock()
	if query, searching := in.lt.transcriptHistorySearch(); searching {
		if in.searchCancel == nil {
			ctx, cancel := context.WithCancel(context.Background())
			in.searchGen++
			gen := in.searchGen
			done := make(chan struct{})
			in.searchCancel = cancel
			in.searchQuery = query
			in.searchDone = done
			go in.pageTranscriptSearch(ctx, cancel, done, gen, query)
		}
		in.mu.Unlock()
		return
	}
	if in.pageInFlight {
		in.mu.Unlock()
		return
	}
	req, need := in.lt.transcriptPageCursor()
	if !need {
		in.mu.Unlock()
		return
	}
	in.pageInFlight = true
	done := make(chan struct{})
	in.pageDone = done
	in.mu.Unlock()
	go in.prefetchTranscriptPages(req, done)
}

func (in *interactiveInput) prefetchTranscriptPages(req transcriptPageRequest, done chan struct{}) {
	defer close(done)
	for {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		if req.fill != nil {
			err = in.lt.fillGap(rctx, *req.fill)
		}
		var messages aria.Page
		if req.fill == nil {
			messages, err = in.readTranscriptPage(rctx, req)
		}
		rcancel()

		in.mu.Lock()
		if err != nil {
			in.lt.transcriptPageFailed()
			in.pageInFlight, in.pageDone = false, nil
			in.mu.Unlock()
			return
		}
		if req.fill != nil {
			in.lt.transcriptFilled()
		} else {
			in.lt.transcriptApplyPage(req, messages)
		}
		if req.seek != 0 && in.lt.tr.jump == nil {
			in.pageInFlight, in.pageDone = false, nil
			in.mu.Unlock()
			return
		}
		next, need := in.lt.transcriptPageCursor()
		if !need || in.lt.transcriptSearchingHistory() {
			in.pageInFlight, in.pageDone = false, nil
			in.mu.Unlock()
			return
		}
		req = next
		in.mu.Unlock()
	}
}

func (in *interactiveInput) pageTranscriptSearch(ctx context.Context, cancel context.CancelFunc, done chan struct{}, gen uint64, query string) {
	defer close(done)
	defer cancel()
	for {
		in.mu.Lock()
		if !in.searchMatchesLocked(gen, query) {
			in.mu.Unlock()
			return
		}
		req, need := in.lt.transcriptPageCursor()
		if !need {
			in.finishSearchWorkerLocked(gen)
			in.mu.Unlock()
			return
		}
		in.mu.Unlock()

		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		var (
			messages aria.Page
			err      error
		)
		if req.fill != nil {
			err = in.lt.fillGap(rctx, *req.fill)
		} else {
			messages, err = in.readTranscriptPage(rctx, req)
		}
		rcancel()

		in.mu.Lock()
		if !in.searchMatchesLocked(gen, query) {
			in.mu.Unlock()
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				in.lt.transcriptPageFailed()
			}
			in.finishSearchWorkerLocked(gen)
			in.mu.Unlock()
			return
		}
		if req.fill != nil {
			in.lt.transcriptFilled()
		} else {
			in.lt.transcriptApplyPage(req, messages)
		}
		if _, searching := in.lt.transcriptHistorySearch(); !searching {
			in.finishSearchWorkerLocked(gen)
			in.mu.Unlock()
			return
		}
		in.mu.Unlock()
	}
}

const ownTurnDeadline = 300 * time.Millisecond

// awaitOwnTurn: true means our question came back on the wire, so no preamble is
// needed.
func awaitOwnTurn(prompt string, own, none <-chan struct{}) bool {
	if strings.TrimSpace(prompt) == "" {
		return true
	}
	t := time.NewTimer(ownTurnDeadline)
	defer t.Stop()
	select {
	case <-own:
		return true
	case <-none:
		return false
	case <-t.C:
		return false
	}
}

func pageCarriesInquiry(p aria.Page, prompt string) bool {
	want := strings.TrimSpace(prompt)
	if want == "" {
		return false
	}
	for _, part := range p.Parts {
		if strings.TrimSpace(part.Inquiry) == want {
			return true
		}
	}
	return false
}

func recentContext(ctx context.Context, fcli transcriptReadClient, cursor int) aria.Page {
	rctx, rcancel := context.WithTimeout(ctx, recentContextTimeout)
	defer rcancel()
	r, err := fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(recentContextMessages))
	if err != nil {
		return aria.Page{}
	}
	history := aria.Page{More: r.More}
	for _, part := range r.Parts {
		if part.Sealed && int(part.ID) <= cursor {
			history.Parts = append(history.Parts, part)
		}
	}
	return history
}

// The client counts messages; the wire spends bytes.
const wireBytesPerMessage = 4096

func wireBudget(messages int) int {
	if messages <= 0 {
		return 0 // the server's default
	}
	return messages * wireBytesPerMessage
}

// historyFetcher is fenced by the subject generation after the read as well as
// before: a switch can land mid-flight, and the page carries the old aria's
// coordinates.
func (in *interactiveInput) historyFetcher() aria.Fetcher {
	gen := atomic.LoadUint64(&in.subjectGen)
	return func(ctx context.Context, before aria.Anchor, limit int) (aria.Page, error) {
		if atomic.LoadUint64(&in.subjectGen) != gen {
			return aria.Page{}, errSubjectChanged
		}
		read := mark.Span("hop.read", "kind", "history", "at", before.Turn)
		r, err := in.fcli.ReadBefore(ctx, before, wireBudget(limit))
		if atomic.LoadUint64(&in.subjectGen) != gen {
			read("parts", 0, "err", true, "stale", true)
			return aria.Page{}, errSubjectChanged
		}
		read("parts", len(r.Parts), "err", err != nil)
		return r, err
	}
}

var errSubjectChanged = errors.New("the transcript changed subject while this read was in flight")

func (in *interactiveInput) readTranscriptPage(ctx context.Context, req transcriptPageRequest) (aria.Page, error) {
	defer mark.Span("hop.read", "kind", "page", "seek", int(req.seek))()
	if req.seek == seekForward {
		return in.fcli.Read(ctx, req.at, 0)
	}
	if req.seek == seekBackward {
		return in.fcli.ReadBefore(ctx, req.at, 0)
	}
	return in.fcli.ReadBefore(ctx, req.at, wireBudget(req.limit))
}

func (in *interactiveInput) searchMatchesLocked(gen uint64, query string) bool {
	current, searching := in.lt.transcriptHistorySearch()
	return gen == in.searchGen && in.searchCancel != nil &&
		in.lt.transcriptActive() && searching &&
		query == in.searchQuery && query == current
}

func (in *interactiveInput) finishSearchWorkerLocked(gen uint64) {
	if gen != in.searchGen {
		return
	}
	in.searchCancel = nil
	in.searchQuery = ""
	in.searchDone = nil
}

func (in *interactiveInput) cancelTranscriptSearchLocked() {
	if in.searchCancel != nil {
		in.searchCancel()
		in.searchCancel = nil
		in.searchQuery = ""
		in.searchDone = nil
		in.searchGen++
	}
	if in.lt.transcriptSearchingHistory() {
		in.lt.transcriptPageFailed()
	}
}

func (in *interactiveInput) cancelTranscriptSearch() {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	in.mu.Unlock()
}

// current is read under the render lock and returns nil until the input loop is wired.
func startPagerClock(mu *renderLock, lt *livelogTurn, current func() *interactiveInput) func() {
	stop := make(chan struct{})
	// Metrics are still pulled: they are not yet an intrinsic form.
	metricsEvery := max(spinnerFPS/metricsPollHz, 1) * 4
	go func() {
		t := time.NewTicker(time.Second / spinnerFPS)
		defer t.Stop()
		for n := 1; ; n++ {
			select {
			case <-stop:
				return
			case <-t.C:
				mu.Lock()
				lt.tick()
				in := current()
				mu.Unlock()
				if in == nil {
					continue
				}
				if n%metricsEvery == 0 {
					in.seedMetrics(in.subjectGeneration())
					in.reseedIfEmpty()
				}
			}
		}
	}()
	return func() { close(stop) }
}

// refreshQueued is the resync path; the last completion wins.
func (in *interactiveInput) refreshQueued() {
	go in.resyncIntrinsic(intrinsicQueue)
}

// inputGate parks the read loop between reads, the only point that neither loses
// bytes nor waits on a keystroke. A pause is requested from the dispatch path, on
// the loop's own goroutine.
type inputGate struct {
	mu     sync.Mutex
	want   bool
	paused chan struct{} // closed by the reader once it has stopped reading
	resume chan struct{} // closed by the suspender to let it read again
}

func (g *inputGate) request() (paused <-chan struct{}, resume chan<- struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.want = true
	g.paused = make(chan struct{})
	g.resume = make(chan struct{})
	return g.paused, g.resume
}

func (g *inputGate) park() {
	g.mu.Lock()
	if !g.want {
		g.mu.Unlock()
		return
	}
	g.want = false
	paused, resume := g.paused, g.resume
	g.mu.Unlock()
	close(paused)
	<-resume
}

// run needs a MakeRaw session so Ctrl-C and Ctrl-D arrive as bytes.
func (in *interactiveInput) run() {
	defer in.cancelSelectionCopy()
	defer in.cancelTranscriptSearch()
	// A debut wake-up outliving the session would paint onto a torn-down
	// transcript.
	defer in.forgetQueueDebuts()
	buf := make([]byte, 4096)
	var pending []byte // a mouse/escape sequence split across reads
	for {
		in.gate.park()
		n, err := in.tc.Read(buf)
		if err != nil {
			in.cancel()
			return
		}
		data := append(pending, buf[:n]...)
		pending = nil
		rest, stop := in.consume(data)
		pending = rest
		if stop {
			return
		}
	}
}

// consume dispatches one chunk as a single frame and returns the tail of an escape
// sequence split across reads.
func (in *interactiveInput) consume(data []byte) (pending []byte, stop bool) {
	in.mu.Lock()
	in.lt.beginTranscriptBatch()
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		in.lt.endTranscriptBatch()
		in.mu.Unlock()
		if in.pageWanted {
			in.pageWanted = false
			in.pageTranscript()
		}
	}()
	i := 0
	for i < len(data) {
		in.mu.Lock()
		mode := in.lt.transcriptMode()
		// Mouse reporting is enabled only with the pager up; parsing for
		// it otherwise swallows a bare Esc as a mouse prefix.
		active := in.lt.transcriptActive()
		in.mu.Unlock()
		// A paste is read before the mouse and the decoder: its payload
		// may contain the escapes both parse.
		if active {
			if text, consumed, ok, need := parseBracketedPaste(data[i:]); need {
				pending = append(pending, data[i:]...)
				break
			} else if ok {
				i += consumed
				in.mu.Lock()
				in.lt.tr.pasteText(text)
				in.mu.Unlock()
				continue
			}
		}
		if active {
			if ev, consumed, ok, need := ldmouse.Parse(data[i:]); need {
				pending = append(pending, data[i:]...)
				break
			} else if ok {
				i += consumed
				delta := 0
				switch ev.Button {
				case ldmouse.WheelUp:
					delta = -3
				case ldmouse.WheelDown:
					delta = 3
				case ldmouse.Left:
					// Press only: the terminal reports the
					// release too, and a toggle would flip
					// twice.
					if ev.Pressed {
						in.clickTranscript(ev)
					}
				}
				if delta != 0 {
					in.mu.Lock()
					in.cancelTranscriptSearchLocked()
					in.lt.transcriptScroll(delta)
					in.mu.Unlock()
					in.pageWanted = true
				}
				continue
			}
		}
		var ev keyEvent
		if key, consumed, ok, need := parseModifiedKey(data[i:]); need {
			pending = append(pending, data[i:]...)
			break
		} else if ok {
			i += consumed
			if letter, isCtrl := ctrlChordLetter(key); isCtrl && ctrlChordBoundIn(mode, letter) {
				// A Ctrl+letter this mode binds keeps its
				// modifiers; every other CSI-u key reduces to the
				// byte it would have arrived as.
				ev = keyEvent{ctrl: letter, shift: key.shift, alt: key.alt, mode: mode}
			} else if key.ctrl && key.code == 0x0d && metaBoundIn(mode, 0x0d) {
				// Ctrl+Enter, which only a CSI-u terminal can
				// report, folds onto Alt+Enter.
				ev = keyEvent{meta: 0x0d, alt: true, mode: mode}
			} else if m, isMeta := metaKey(key); isMeta && (metaBoundIn(mode, m) || metaOpens(m)) {
				// Alt+<key> with the Alt bit set: what a legacy
				// terminal spells ESC <byte>.
				ev = keyEvent{meta: m, shift: key.shift, alt: true, mode: mode}
			} else {
				b, representable := key.asByte()
				if !representable {
					continue
				}
				if in.coalesceNewline(b) {
					continue
				}
				ev = keyEvent{b: b, mode: mode}
			}
		} else if data[i] == 0x1b {
			// Delimit the sequence (arrows, SS3, OSC replies, CSI) so
			// a prefix does not fire bare Esc.
			if ec, en := consumeEscapeSequence(data[i:]); en {
				pending = append(pending, data[i:]...)
				break
			} else if ec > 0 {
				seq := data[i : i+ec]
				i += ec
				in.lastNL = 0
				key, ok := navKeyFor(seq)
				if !ok {
					continue
				}
				if m, isMeta := metaForCtrlArrow(key); isMeta && metaBoundIn(mode, m) {
					// Ctrl+Left/Right are the word motions, as
					// every inputrc binds them.
					ev = keyEvent{meta: m, alt: true, mode: mode}
				} else {
					ev = keyEvent{nav: key.nav, shift: key.shift, alt: key.alt, mode: mode}
				}
			} else if m, isMeta := metaEscapePrefix(data[i:], mode); isMeta {
				// ESC <byte> is Alt+<byte>, claimed only where a
				// row binds it.
				i += 2
				in.lastNL = 0
				ev = keyEvent{meta: m, alt: true, mode: mode}
			} else {
				b := data[i]
				i++
				if in.coalesceNewline(b) {
					continue
				}
				ev = keyEvent{b: b, mode: mode}
			}
		} else {
			b := data[i]
			i++
			if in.coalesceNewline(b) {
				continue
			}
			ev = keyEvent{b: b, mode: mode}
		}
		if mark.Enabled() {
			mark.Mark("key", "chord", ev.chord().String(), "mode", int(ev.mode))
		}
		if ev.mode == modeIncipit && opensTranscript(ev) {
			in.enterTranscript()
			in.mu.Lock()
			ev.mode = in.lt.transcriptMode()
			in.mu.Unlock()
		}
		in.mu.Lock()
		var armed *keyBinding
		if in.lt.tr.pending != "" {
			if armed = in.lt.tr.prefixed(ev); armed == nil {
				in.lt.tr.pending = ""
			}
		}
		in.mu.Unlock()
		var act inputFunc
		switch {
		case armed != nil && armed.input != nil:
			in.mu.Lock()
			in.lt.tr.pending = ""
			in.mu.Unlock()
			act = armed.input
		case armed == nil:
			act = inputAct.input(ev.mode, ev)
		}
		if act != nil {
			if act(in, ev) == keyStop {
				return pending, true
			}
			continue
		}
		if ev.mode != modeIncipit {
			in.mu.Lock()
			in.cancelTranscriptSearchLocked()
			in.lt.transcriptDispatch(ev)
			in.mu.Unlock()
			in.pageWanted = true
		}
	}
	return pending, false
}

// The input-level key actions run on the read goroutine and take the render lock
// themselves.

// inputInterrupt is Ctrl-C: a state machine over the selection and the clipboard,
// in this order.
func inputInterrupt(in *interactiveInput, ev keyEvent) keyVerdict {
	if ev.mode != modeIncipit {
		in.mu.Lock()
		in.cancelTranscriptSearchLocked()
		plan, selected := in.lt.transcriptSelectionPlan()
		if selected && in.copyFailed &&
			in.copyFailedLo == plan.lo && in.copyFailedHi == plan.hi {
			in.copyFailed = false
			in.lt.clearTranscriptSelection()
			in.mu.Unlock()
			in.cancelSelectionCopy()
			in.cancel()
			return keyStop
		}
		if selected && in.copyCancel != nil {
			in.copyCancel()
			in.copyCancel = nil
			in.copyGen++
			in.copyFailed = true
			in.copyFailedLo, in.copyFailedHi = in.copyPlan.lo, in.copyPlan.hi
			in.copyPlan = selectionCopyPlan{}
			in.mu.Unlock()
			return keyHandled
		}
		if selected {
			copyCtx, copyCancel := context.WithTimeout(context.Background(), 30*time.Second)
			in.copyGen++
			gen := in.copyGen
			in.copyCancel = copyCancel
			in.copyPlan = plan
			in.copyFailed = false
			in.mu.Unlock()
			go in.copySelection(copyCtx, copyCancel, gen, plan)
			return keyHandled
		}
		in.mu.Unlock()
	}
	in.cancelTranscriptSearch()
	in.cancelSelectionCopy()

	if in.hangup == nil {
		in.cancel()
		return keyStop
	}
	in.mu.Lock()
	already := in.interrupting
	in.interrupting = true
	in.mu.Unlock()
	if already {
		in.cancel()
		return keyStop
	}
	in.mu.Lock()
	in.lt.report("interrupting")
	in.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := in.hangup.Hangup(ctx, rpc.QueueKeep)
		if err == nil && !resp.Stopped {
			// Nothing will close doneCh.
			in.cancel()
			return
		}
		if err != nil {
			in.mu.Lock()
			in.lt.reportError("interrupt failed: " + err.Error())
			in.mu.Unlock()
			in.cancel()
		}
	}()
	return keyHandled
}

func inputHangUp(in *interactiveInput, _ keyEvent) keyVerdict {
	return in.hangUp(rpc.QueueKeep)
}

func inputHangUpDrop(in *interactiveInput, _ keyEvent) keyVerdict {
	return in.hangUp(rpc.QueueClear)
}

// hangUp stops the turn and stays.
func (in *interactiveInput) hangUp(disposition rpc.QueueDisposition) keyVerdict {
	if in.hangup == nil {
		in.mu.Lock()
		in.lt.report("nothing to interrupt")
		in.mu.Unlock()
		return keyHandled
	}
	in.mu.Lock()
	in.lt.report("interrupting")
	in.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := in.hangup.Hangup(ctx, disposition)
		in.mu.Lock()
		defer in.mu.Unlock()
		switch {
		case err != nil:
			in.lt.reportError("interrupt failed: " + err.Error())
		case resp.Cleared && len(resp.Queue) > 0:
			in.lt.showDropped(resp.Queue)
		case resp.Stopped:
			in.lt.report("interrupted")
		default:
			in.lt.report("nothing to interrupt")
		}
	}()
	return keyHandled
}

func inputLeavePit(in *interactiveInput, ev keyEvent) keyVerdict {
	in.mu.Lock()
	open := in.lt.tr.pit.open()
	if open {
		in.lt.tr.closePanels()
		in.lt.render()
	}
	in.mu.Unlock()
	if open {
		return keyHandled
	}
	return inputDisconnect(in, ev)
}

func inputDisconnect(in *interactiveInput, _ keyEvent) keyVerdict {
	in.cancelTranscriptSearch()
	in.cancelSelectionCopy()
	select {
	case in.disconnectCh <- struct{}{}:
	default:
	}
	return keyStop
}

func inputEnterTranscript(in *interactiveInput, _ keyEvent) keyVerdict {
	in.cancelTranscriptSearch()
	in.enterTranscript()
	return keyHandled
}

func inputToggleVerbose(in *interactiveInput, _ keyEvent) keyVerdict {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	in.set.verbose = !in.set.verbose
	in.lt.invalidateTranscriptRows()
	in.lt.render()
	in.mu.Unlock()
	return keyHandled
}

func inputToggleSticky(in *interactiveInput, _ keyEvent) keyVerdict {
	in.mu.Lock()
	in.set.sticky = !in.set.sticky
	in.lt.render()
	in.mu.Unlock()
	return keyHandled
}

func inputQuestionNext(in *interactiveInput, _ keyEvent) keyVerdict {
	return in.questionKey(1)
}

func inputQuestionPrev(in *interactiveInput, _ keyEvent) keyVerdict {
	return in.questionKey(-1)
}

func (in *interactiveInput) questionKey(delta int) keyVerdict {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	in.lt.transcriptJumpQuestion(delta)
	in.pageWanted = in.lt.tr.jump != nil
	in.mu.Unlock()
	return keyHandled
}

func inputYank(in *interactiveInput, ev keyEvent) keyVerdict {
	if ev.mode != modeIncipit {
		in.mu.Lock()
		if in.lt.tr.box == boxComposer && in.lt.tr.composerMode == composerNormal {
			text, ok := in.lt.tr.composerYankText()
			if !ok {
				in.lt.tr.setCommandNoteAt("nothing to copy", alertError)
				in.lt.tr.render()
				in.mu.Unlock()
				return keyHandled
			}
			in.lt.tr.noteYank(text)
			in.lt.tr.render()
			in.mu.Unlock()
			in.tc.SetClipboard(text)
			return keyHandled
		}
		if row, ok := in.lt.tr.pit.selected(); ok && row.yank != "" {
			in.lt.tr.noteYank(row.yank)
			in.mu.Unlock()
			in.tc.SetClipboard(row.yank)
			return keyHandled
		}
		// A visual selection is answered from the painted rows: no wire.
		if in.lt.tr.visual.active() {
			text := in.lt.tr.visualText()
			if !in.lt.tr.visual.highlighted() || text == "" {
				in.lt.tr.setCommandNoteAt("no highlight (v or V at the cursor to start one)", alertError)
				in.lt.tr.render()
				in.mu.Unlock()
				return keyHandled
			}
			in.lt.tr.noteYank(text)
			in.lt.tr.render()
			in.mu.Unlock()
			in.tc.SetClipboard(text)
			return keyHandled
		}
		plan, selected := in.lt.transcriptSelectionPlan()
		if selected && in.copyCancel == nil && !in.copyFailed {
			copyCtx, copyCancel := context.WithTimeout(context.Background(), 30*time.Second)
			in.copyGen++
			gen := in.copyGen
			in.copyCancel = copyCancel
			in.copyPlan = plan
			in.mu.Unlock()
			go in.copySelection(copyCtx, copyCancel, gen, plan)
			return keyHandled
		}
		in.mu.Unlock()
	}
	in.mu.Lock()
	name := ariaName(in.figaroID)
	if ev.mode != modeIncipit {
		in.lt.tr.noteYank(name)
		in.lt.tr.render()
	}
	in.mu.Unlock()
	in.tc.SetClipboard(name)
	return keyHandled
}

func inputYankID(in *interactiveInput, ev keyEvent) keyVerdict {
	in.mu.Lock()
	id, name := in.figaroID, ariaName(in.figaroID)
	if ev.mode != modeIncipit {
		if row, ok := in.lt.tr.pit.selected(); ok && row.aria != "" {
			id, name = row.aria, cmp.Or(row.yank, ariaName(row.aria))
		}
		in.lt.tr.setCommandNote("yanked the id of " + name)
		in.lt.tr.render()
	}
	in.mu.Unlock()
	in.tc.SetClipboard(id)
	return keyHandled
}

func inputYankCoordinate(in *interactiveInput, _ keyEvent) keyVerdict {
	in.mu.Lock()
	token, note, err := in.lt.tr.visualCoordinate()
	switch {
	case err != "":
		in.lt.tr.setCommandNoteAt("range: "+err, alertError)
	case note != "":
		in.lt.tr.setCommandNote("yanked " + token + " (" + note + ")")
	default:
		in.lt.tr.setCommandNote("yanked " + token)
	}
	in.lt.tr.render()
	in.mu.Unlock()
	if err == "" {
		in.tc.SetClipboard(token)
	}
	return keyHandled
}

// Input-level because only the CSI-u report carries the modifier; the raw
// 0x0e/0x10 bytes go through the pager.
func inputSelectNext(in *interactiveInput, ev keyEvent) keyVerdict {
	return in.selectNodeKey(1, ev)
}

func inputSelectPrev(in *interactiveInput, ev keyEvent) keyVerdict {
	return in.selectNodeKey(-1, ev)
}

func (in *interactiveInput) selectNodeKey(delta int, ev keyEvent) keyVerdict {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	if ev.shift {
		in.lt.transcriptJumpQuestion(delta)
	} else {
		in.lt.transcriptSelect(delta, ev.alt)
	}
	in.mu.Unlock()
	in.pageWanted = true
	return keyHandled
}

func (in *interactiveInput) clickTranscript(ev ldmouse.Event) {
	row := ev.Y - 1 // the report is 1-based
	acted := false
	in.mu.Lock()
	if in.lt.transcriptClickable(row) {
		in.cancelTranscriptSearchLocked()
		acted = in.lt.transcriptClick(row, ev.Shift())
	}
	in.mu.Unlock()
	if acted {
		in.pageWanted = true
	}
}

func (in *interactiveInput) copySelection(ctx context.Context, cancel context.CancelFunc, gen uint64, plan selectionCopyPlan) {
	text, err := selectionText(plan, transcriptPageSize, func(at aria.Anchor, limit int) (aria.Page, error) {
		return in.fcli.ReadBefore(ctx, at, wireBudget(limit))
	})
	cancel()
	in.mu.Lock()
	if gen != in.copyGen {
		in.mu.Unlock()
		return
	}
	in.copyCancel = nil
	in.copyPlan = selectionCopyPlan{}
	current, active := in.lt.transcriptSelectionPlan()
	if err == nil && active && current.lo == plan.lo && current.hi == plan.hi {
		in.lt.clearTranscriptSelection()
	}
	if err == nil {
		in.copyFailed = false
		in.tc.SetClipboard(text)
	} else {
		in.copyFailed = true
		in.copyFailedLo, in.copyFailedHi = plan.lo, plan.hi
	}
	in.mu.Unlock()
}

func (in *interactiveInput) cancelSelectionCopy() {
	in.mu.Lock()
	if in.copyCancel != nil {
		in.copyCancel()
		in.copyCancel = nil
		in.copyPlan = selectionCopyPlan{}
		in.copyGen++
	}
	in.mu.Unlock()
}

// coalesceNewline reports the second half of a CR+LF or LF+CR pair, which Windows
// conhost emits for one Enter.
func (in *interactiveInput) coalesceNewline(b byte) bool {
	if (b == 0x0d || b == 0x0a) && in.lastNL != 0 && in.lastNL != b {
		in.lastNL = 0
		return true
	}
	if b == 0x0d || b == 0x0a {
		in.lastNL = b
	} else {
		in.lastNL = 0
	}
	return false
}

func dimRule() string { return term.Dim(strings.Repeat("─", termWidth())) }

// endSession is the last write: the shell prompts where the cursor is left, and a
// lone CR adds no row.
func endSession(w io.Writer) {
	fmt.Fprint(w, "\r"+cursorShow+autowrapOn)
}

func termWidth() int {
	if w := term.Width(); w > 0 {
		return w
	}
	return 80
}

const metricsPollHz = 2

func inputToggleBarVerbose(in *interactiveInput, _ keyEvent) keyVerdict {
	in.mu.Lock()
	in.lt.status.toggleVerbose()
	in.lt.render()
	in.mu.Unlock()
	return keyHandled
}
