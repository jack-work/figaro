package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

const spinnerFPS = 11 // spinner frames per second (~90ms/frame)

// recentCursor is a beyond-the-end turn id. ReadBefore from it returns the
// newest byte-bounded node page: the pager's initial lazy window.
const recentCursor = 1 << 60

// The opening preamble: how much history a new prompt is allowed to fetch, and
// how long it may wait for it.
//
// The budget is small on purpose: the preamble is orientation, not a
// transcript, and the renderer clips it to half the viewport anyway. The
// timeout is the UI contract: the agent socket serves one request at a time,
// so this read sits between the prompt's accept and its first painted row.
// enterTranscript can afford 5s because the user asked for the pager and it
// has nothing else to draw; a prompt cannot, so a slow read is abandoned and
// the session opens exactly as it did before.
const (
	recentContextMessages = 4
	recentContextTimeout  = 300 * time.Millisecond
)

// Terminal control: disable auto-margin (so a full-width row never wraps) and
// hide the cursor while the renderer owns the screen.
const (
	autowrapOff = "\x1b[?7l"
	autowrapOn  = "\x1b[?7h"
	cursorHide  = "\x1b[?25l"
	cursorShow  = "\x1b[?25h"
)

// mustPromptFigaro is the interactive (TTY) prompt path. It renders the
// turn-shaped aria.Page wire through the incipit-freeze renderer: closed turn
// slices freeze to native scrollback once and are never redrawn; only the open
// suffix is a live region. The renderer folds each frame and animates spinners
// locally (no extra wire traffic).
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded, set renderSettings) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	startedAt := time.Now()
	status := newSessionStatus(figaroID, startedAt)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	height := term.Height()

	// Bookend: a status rule (aria id + start time) pinned just below the
	// agent's reply. Gated on the status-line config.
	var bookendFn func() []string
	if loaded.StatusLine() {
		bookendFn = func() []string { return bookendLines(status) }
	}

	lt := newLivelogTurn(os.Stdout, width, height, &set, figaroID, startedAt, status, bookendFn, dimRule)
	tc := term.NewClient() // platform terminal boundary: raw mode, resize, clipboard

	// The renderer owns the cursor and assumes one row per line: disable the
	// terminal's auto-margin so a full-width row never wraps, and hide the
	// cursor. It draws in incipit (no alternate screen): frozen output lands in
	// the normal scrollback.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer endSession(os.Stdout)
	// The painter's half of the console arming: hold the cursor on the last
	// cell instead of wrapping the instant it is written. Scoped to the painted
	// session, armed globally it staircases every fmt.Println. OnceFunc for the
	// same reason as cli.go's: the defer and exitNow's hooks both fire.
	restoreWrap := sync.OnceFunc(term.ArmDeferredWrap())
	defer restoreWrap()
	atExit(restoreWrap)
	defer lt.leaveTranscript() // restore the screen if we exit while in the pager

	// Static opening rule: a single dim horizontal line separating the user's
	// shell prompt from the response stream. Printed once, lives in scrollback.
	// The renderer owns it, because it also owns the decision about the blank
	// row underneath it: the first message hugs this rule as its overline.
	lt.openRule()

	// The renderer is single-threaded; the notify pump, the spinner ticker, the
	// SIGWINCH handler, and keybindings all serialize on mu.
	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1) // Ctrl-D: leave the turn running

	// THE DEFERS ABOVE DO NOT RUN ON os.Exit, and two exits here are os.Exit:
	// die() (a Qua failure lands AFTER --listen has already opened the pager)
	// and the Ctrl-C 130 path. Both would leave the terminal on the alternate
	// screen with the cursor hidden. Same work, registered where an abrupt
	// exit can still reach it.
	atExit(func() {
		// TryLock, not Lock: the hook must never be the reason a dying
		// process hangs. Every current caller is lock-free at the point it
		// dies, so this takes the lock in practice, and if a future one is
		// not, restoring the terminal unlocked still beats not restoring it.
		if mu.TryLock() {
			defer mu.Unlock()
		}
		lt.leaveTranscript()
		endSession(os.Stdout)
	})

	// mu serializes every renderer entry point; handing it to the pager arms
	// the frame-rate ceiling, whose trailing repaint runs on a timer goroutine
	// and so needs the same lock.
	lt.setRenderLock(&mu)
	running := true  // a turn is in flight until turn.done; gates Ctrl-C
	sendCursor := -1 // cursor from Qua; stop only once committed past it and idle

	// THE DISCRIMINATOR. ownTurn is closed the instant a frame carries the
	// inquiry we just sent: proof that OUR prompt opened the turn about to
	// paint. noTurn is closed when the agent reports an error instead, because
	// then no turn is coming at all. State can race (Qua's `active` flag is
	// sampled before the prompt is even queued); the EVENT cannot, and it is
	// the same split the daemon itself makes: appendUserPrompt RETURNS before
	// OpenInquiry when it is steering, so a turn we merely joined never
	// broadcasts a question of ours.
	ownTurn := make(chan struct{})
	noTurn := make(chan struct{})
	var ownOnce, noOnce sync.Once
	// watchInquiry keeps the check OFF the steady-state frame path. Every aria
	// frame passes through onNotify: dozens a second while a tool streams, and
	// the question is only ever asked once, at the opening of the session.
	// Cleared by the answer, and again by the decision itself, so from then on a
	// frame pays one bool test. (Touched only under mu, like everything else
	// here.)
	watchInquiry := true

	onNotify := func(method string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case rpc.MethodAriaFrame:
			var r aria.Page
			if json.Unmarshal(params, &r) == nil {
				if watchInquiry && pageCarriesInquiry(r, prompt) {
					watchInquiry = false
					ownOnce.Do(func() { close(ownTurn) })
				}
				lt.apply(r)
			}
		case rpc.MethodTurnDone:
			var d rpc.DoneEntry
			_ = json.Unmarshal(params, &d)
			isErr := strings.HasPrefix(d.Reason, "error:")
			// Settle when the agent reports idle (inbox empty, no turn running):
			// a turn that ended with our steer still queued reports idle=false,
			// so we correctly wait for our own turn. A daemon predating the idle
			// field sends nil: treat that as settled (the pre-steering behavior),
			// so an old running daemon doesn't strand the command. We only act
			// once our prompt has been submitted (sendCursor set after Qua
			// returns), so a turn.done that predates our send can't end us early.
			// Do NOT gate on lt.cursor() advancing: the final commit can arrive
			// via async desync recovery AFTER this one-shot turn.done, which
			// would strand us and hang the command.
			idle := d.Idle == nil || *d.Idle
			// Tear the live region (incl. an un-adopted thinking footer) down
			// FIRST, so an error hint printed straight to the terminal lands on
			// clean scrollback below it, not over the footer.
			lt.finishTurn(d.Reason)
			if isErr {
				// No turn will open for this prompt, so stop waiting on an
				// inquiry that is never coming.
				noOnce.Do(func() { close(noTurn) })
				// THE COMMENT ABOVE IS TRUE INLINE AND FALSE IN THE PAGER:
				// finishTurn returns early while the pager is up, the alt
				// screen has no scrollback, and the cursor is parked on the
				// status row: so writing here landed the hint ON the footer
				// and scrolled the grid out from under the painter's model.
				// lt.report picks the right door for whichever renderer owns
				// the terminal, and loses nothing either way.
				if hint, ok := authFailureHint(d.Reason); ok {
					lt.report(hint)
				} else {
					lt.report(d.Reason)
				}
			}
			settled := sendCursor >= 0 && idle
			if !settled {
				break
			}
			running = false
			// Close on turn-done only in incipit. Once the transcript pager is
			// up: however it was entered: it has listen semantics: the session
			// stays open until an explicit q / Ctrl-D / Ctrl-C.
			//
			// NOTE for anyone adding work here: onNotify already holds mu with a
			// deferred unlock, and mu is a plain sync.Mutex: NOT reentrant.
			// Re-locking it on this path once deadlocked the notify pump, and the
			// input loop with it (rendering needs the same lock), so every exit
			// key went dead and the process could never be closed.
			if !lt.inTranscript() {
				select {
				case doneCh <- struct{}{}:
				default:
				}
			}
		}
	}

	// Hold frames from before the socket is dialed until the opening preamble
	// (below) has been placed. A subscription starts pushing the moment it is
	// made, so this is the only way to guarantee the question is painted UNDER
	// the history it follows rather than over it. Nothing is dropped.
	lt.holdFrames()

	// The wire tape (testing): --record tees every JSON-RPC message this
	// stream exchanges, so a turn that painted wrong can be replayed exactly.
	var rec *tape.Writer
	if set.record != "" {
		var terr error
		rec, terr = tape.Create(set.record, tape.Header{
			Aria:    figaroID,
			Cols:    term.Width(),
			Rows:    term.Height(),
			Term:    os.Getenv("TERM"),
			Binary:  buildRevision(),
			Command: strings.Join(os.Args, " "),
		})
		if terr != nil {
			die("record: %s", terr)
		}
		defer func() {
			if cerr := rec.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "figaro: tape: %v\n", cerr)
			}
		}()
	}

	fcli, err := figaro.DialClientWith(ep, onNotify, tapeTap(rec))
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	// On a version desync, re-read from the highest fully sealed turn and re-apply
	// the full snapshot (off the notify path so the pump isn't blocked).
	lt.setDesync(func(sinceLT int) {
		go func() {
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rcancel()
			r, rerr := fcli.Read(rctx, sinceLT)
			if rerr != nil {
				return
			}
			mu.Lock()
			lt.apply(r)
			mu.Unlock()
		}()
	})

	// Local spinner animation: ticks the open message's running tool; zero extra
	// wire traffic (output streams via aria frames).
	// Declared before the ticker so the queue poll can reach it; both accesses
	// are under mu, which every renderer path already holds.
	var in *interactiveInput

	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / spinnerFPS)
		defer t.Stop()
		// The queue is polled on a slow multiple of the spinner rather than
		// pushed. figaro.queued is a per-aria RPC over a unix socket and the
		// answer is a snapshot of the agent's inbox, so there is nothing to
		// subscribe to; a prompt enters the queue without any frame being
		// emitted, which is precisely why it used to be invisible. Twice a
		// second is faster than a human notices and cheap enough not to matter.
		queueEvery := spinnerFPS / queuedPollHz
		if queueEvery < 1 {
			queueEvery = 1
		}
		n := 0
		for {
			select {
			case <-stopTick:
				return
			case <-t.C:
				mu.Lock()
				lt.tick()
				poll := in
				mu.Unlock()
				if n++; n%queueEvery == 0 && poll != nil {
					poll.refreshQueued()
				}
			}
		}
	}()
	defer close(stopTick)

	// Repaint on resize (platform-abstracted: SIGWINCH on unix, a console event
	// on Windows, all behind the term.Client boundary).
	defer tc.OnResize(func(w, h int) {
		mu.Lock()
		lt.resize(w, h)
		mu.Unlock()
	})()

	// Live keybindings. MakeRaw disables signal generation, so Ctrl-C (0x03) and
	// Ctrl-D (0x04) arrive as input BYTES (portable, and identical in incipit and
	// transcript): the input loop owns them, not a SIGINT handler.
	if tc.IsTTY() {
		if restore, err := tc.MakeRaw(); err == nil {
			// The restore must survive os.Exit, not only a normal return.
			// MEASURED on Linux before this line existed: Ctrl-C during a
			// running turn exits 130 through exitNow, which runs the hooks and
			// then os.Exit: skipping every defer. `stty -g` before/after
			// differed in c_lflag (8a3b -> a30: ECHO and ICANON cleared), so
			// the user's shell was left in RAW MODE: no echo, no line editing
			//: by the most ordinary gesture there is.
			//
			// The atExit hook above already restores the screen, the cursor and
			// autowrap for exactly this reason; the terminal MODE was the one
			// piece still left on a defer. On Windows this same closure now
			// also restores the console OUTPUT mode (VT processing +
			// DISABLE_NEWLINE_AUTO_RETURN), so leaving it on a defer would leak
			// a changed right-edge wrap policy into the shell after a Ctrl-C.
			//
			// sync.OnceFunc because both paths fire on a normal return: the
			// defer runs, and then exitNow's hooks would run it again.
			restoreOnce := sync.OnceFunc(restore)
			defer restoreOnce()
			atExit(restoreOnce)
			fmt.Fprint(os.Stdout, enableModifiedKeyReporting)
			defer fmt.Fprint(os.Stdout, disableModifiedKeyReporting)
			// Belt-and-braces: always disable mouse reporting on exit so a crash
			// mid-pager can't leave the shell spewing raw \x1b[<…M.
			defer os.Stdout.WriteString(ldmouse.Disable)
			in = &interactiveInput{
				tc: tc, lt: lt, fcli: fcli, hangup: fcli, mu: &mu, set: &set,
				figaroID: figaroID, cancel: cancel, disconnectCh: disconnectCh,
			}
			in.lt.setQueuedFetch(in.refreshQueued) // 'Q' works before the pager is ever opened
			// Both are inert until the PAGER is up (a fetcher is only consulted by
			// Store.Ensure, i.e. the prefetch worker), and both must be in place
			// BEFORE the first frame, because the pager can open without being
			// asked: an overflowing turn or a destructive resize promotes on the
			// frame path, and a pager that opened that way used to have neither a
			// way to ask for history nor any history to show.
			in.lt.setHistoryFetcher(in.historyFetcher())
			in.lt.setCatchUp(in.pagerCatchUp)
			if set.listen {
				in.enterTranscript() // --listen: open the pager immediately
			}
			go in.run()
		} else {
			fmt.Fprintf(os.Stderr, "figaro: terminal input disabled: enter raw mode: %v\n", err)
		}
	}

	// The opening preamble is for the INLINE renderer only: --listen already
	// opened the pager (which reads its own history), and a non-TTY caller gets
	// a stream, not a screen, so neither may grow a spurious read or a row of
	// output it did not have before.
	catchUp := in != nil && !set.listen

	cursor, active, qerr := fcli.Qua(ctx, prompt, buildPromptForm())
	if qerr != nil {
		dieWithClosure(qerr, "prompt: %s", qerr)
	}
	mu.Lock()
	sendCursor = cursor
	lt.status.beginTurn()
	mu.Unlock()
	// Joining an already-running turn: the inline renderer can't cleanly paint a
	// turn already in progress (partial state, mid-stream). Drop into the
	// transcript pager on the last page: consistent scrollback, no glitch. A
	// fresh turn (idle aria) stays inline with the thinking footer.
	joined := active && in != nil
	if joined {
		in.enterTranscript()
	}

	// WHERE YOU ARE: but only when you did not just say it yourself.
	//
	// An aria with history used to open on a dim rule and nothing else until
	// the model's first token. Printing the tail fixes that, but printing it on
	// EVERY prompt restates the previous exchange immediately above itself,
	// separated only by the shell prompt, and `q` in a continuous terminal is
	// how this is mostly driven, so that is a daily irritant, not a corner.
	//
	// The rule: a prompt to an IDLE aria is call-response: nothing before the
	// question. A prompt that lands on a turn we did not open buys a little
	// context, which is both printed (orientation) and KEPT (openInline seeds
	// the pager with it, so Ctrl-T opens on history without a read). Decided
	// from the STREAM: did our own question come back?: not from Qua's
	// `active`, which is sampled before the prompt is even queued.
	var fetched historyPage
	if catchUp && !joined && !awaitOwnTurn(prompt, ownTurn, noTurn) {
		fetched = recentContext(ctx, fcli, cursor)
	}
	mu.Lock()
	watchInquiry = false // decided; no frame need ask again
	lt.openInline(fetched)
	mu.Unlock()

	select {
	case <-doneCh:
		// The committed bookend is the final line; nothing more to print.
	case <-disconnectCh:
		// q / Ctrl-D. If the turn already finished while the pager was up this
		// is a clean exit, not an abandonment: no rule, no follow hint, and the
		// completed tail reaches scrollback intact.
		lt.abandon(turnStatusDisconnected)
	case <-fcli.Done():
		lt.abandon(turnStatusError)
		os.Exit(1)
	case <-ctx.Done():
		// Ctrl-C: interrupt the in-flight turn; if nothing's running (e.g.
		// listening after turn-done), it's just a clean close.
		mu.Lock()
		wasRunning := running
		mu.Unlock()
		if wasRunning {
			// Same door as the error path: in the pager this is a red token in
			// the status row, inline it is the line on stderr it always was.
			mu.Lock()
			lt.report("interrupting...")
			mu.Unlock()
			intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = fcli.Interrupt(intCtx)
			intCancel()
			select {
			case <-doneCh:
			case <-fcli.Done():
			case <-time.After(3 * time.Second):
				lt.abandon(turnStatusInterrupted)
			}
			mu.Lock()
			lt.report("interrupted")
			mu.Unlock()
		}
		if code := interruptExit(wasRunning); code != 0 {
			exitNow(code) // hooks first: the pager may still be up
		}
	}
}

// interruptExit is the Ctrl-C rule: an interrupted TURN is 130 (128+SIGINT),
// a Ctrl-C with nothing running is a clean 0. This path used to fall out of
// its select and exit 0, so `send … || retry` never fired. Ctrl-D stays 0 -
// it leaves the turn running on purpose.
func interruptExit(wasRunning bool) int {
	if wasRunning {
		return exitInterrupted
	}
	return 0
}

// interactiveInput is the shared control-key + pager input loop for the live
// TTY commands: send's mustPromptFigaro and listen's tailFigaro. It owns
// Ctrl-C/D/L/T/O and 'y' (copy id), plus the pager's scroll + mouse, so both
// commands behave identically in incipit and transcript.
type interactiveInput struct {
	tc   term.Client
	lt   *livelogTurn
	fcli transcriptReadClient
	// interrupting is set by the first Ctrl-C: the turn has been asked to
	// stop and we are waiting for turn.done to close us. A second Ctrl-C
	// leaves immediately, because a user must never be trapped by a daemon
	// that will not answer.
	interrupting bool
	// hangup is the H key's client. Nil leaves the key inert rather than
	// crashing a session that was built without one.
	hangup       hangupClient
	mu           *sync.Mutex
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
	// pageInFlight guards the single background history fetch. Page fetches are
	// asynchronous so that scrolling never waits on an RPC; pageDone is closed
	// when the worker finishes (tests synchronize on it).
	pageInFlight bool
	pageDone     chan struct{}
	// pageWanted defers the history-paging check to the end of the input chunk.
	// It reads a viewport offset that render() clamps, so it belongs after the
	// frame: once per chunk, not once per event. (B wrote this when
	// pageTranscript still blocked on the RPC; D since made it a non-blocking
	// single-flight launcher, so the deferral is now about asking the *settled*
	// viewport for a cursor exactly once, instead of once per wheel report.)
	// Only ever touched on the input goroutine.
	pageWanted bool
	// caughtUp records that this session has read the aria's tail into the store
	// once. Guarded by mu (pagerCatchUp is called with it already held). See
	// pagerCatchUp: a failed read clears it, because a timeout is not a floor.
	caughtUp bool
	// lastNL is the last CR/LF byte we delivered (0 otherwise). Windows
	// conhost and some other terminals emit CR+LF for a single Enter press;
	// without dedup, a toggle-style binding (Enter -> expand tools) fires
	// twice and cancels itself out. Also covers the rare LF+CR order.
	lastNL byte
}

// hangupClient is the one capability the H key needs. It is deliberately NOT
// folded into transcriptReadClient: a stub that only serves history has no
// business growing a method it will never call, and the ten of them in the
// tests are evidence enough that the two roles are separate.
type hangupClient interface {
	Hangup(context.Context, rpc.QueueDisposition) (*rpc.InterruptResponse, error)
}

type transcriptReadClient interface {
	Read(context.Context, int) (aria.Page, error)
	ReadBefore(context.Context, aria.Anchor, int) (aria.Page, error)
	Queued(context.Context) (*rpc.QueuedResponse, error)
}

// enterTranscript opens the pager on the recent window (older history pages in
// on scroll-up); shared by Ctrl-T, Ctrl-L, and listen's auto-enter. No-op when
// already in the pager.
//
// One read suffices. A backward read from a beyond-the-end cursor is the tail,
// and because that window reaches the last node of the last turn it carries the
// open suffix with it: so a viewer joining mid-turn gets the live message's
// base version in the same page. (This used to need a second, forward read:
// locate clamped a beyond-the-end anchor to the last turn's FIRST node, so the
// backward window stopped short of the live suffix and the forward one returned
// the whole turn again: which is how a dormant aria rendered its last turn
// twice.)
//
// This is the DELIBERATE door, so the read is synchronous: the reader asked for
// the transcript and has nothing to look at until it lands. The automatic
// promotions cannot afford that (they run under the render lock, on the frame
// path and the resize handler) and take pagerCatchUp instead.
func (in *interactiveInput) enterTranscript() {
	in.mu.Lock()
	already := in.lt.transcriptActive()
	seeded := in.lt.hasSeed()
	in.mu.Unlock()
	if already {
		return
	}
	// Already holding the catch-up page (we joined a turn we did not open, and
	// the inline view fetched the tail to orient with): open on it rather than
	// blocking the input loop on a read for content we have. Scrolling up still
	// pages older history in, asynchronously, as it always did.
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
	r, rerr := in.fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(transcriptPageSize))
	rcancel()
	in.mu.Lock()
	// Claimed BEFORE the pager opens: enterPager fires the catch-up hook for a
	// seedless pager, and this door has just done that read itself.
	in.caughtUp = rerr == nil
	in.lt.enterTranscript()
	in.lt.setQueuedFetch(in.refreshQueued)
	in.lt.setHistoryFetcher(in.historyFetcher())
	if rerr == nil {
		in.lt.apply(r)
		// The wire's own answer to "is there anything before this page", which
		// is the only honest source for it: the pager reads it back as "can I
		// still page older history" instead of latching a bit of its own.
		in.lt.setMoreBefore(r.More.Before)
	}
	in.mu.Unlock()
}

// pagerCatchUp is the read the AUTOMATIC promotions owe, armed on the
// livelogTurn and fired from enterPager.
//
// THE BUG IT EXISTS FOR. Ctrl-T and `figaro listen` read history before they
// open the pager. The pager also opens BY ITSELF, an open turn taller than the
// viewport (OnLive → openOverflows), or a resize that shrinks the viewport
// under the live region, and those two doors read nothing. The store then held
// exactly one turn: the one being streamed. With MoreBefore never set by any
// wire answer it stays false, so transcript.atAriaFloor concluded that the
// question the user had just typed WAS the beginning of the aria: no history
// above it, no page ever requested by pageCursor, and scrolling up found
// nothing, forever. The conversation was intact on disk and one `figaro listen`
// away; only this view could not see it. It is the ordinary case on a small
// window, which is why it was reported from Windows first.
//
// It must not block: both callers hold the render lock, and one of them is the
// frame path. So it claims the read under the lock the caller already holds,
// and performs it on its own goroutine.
//
// A FAILED READ IS NOT A FLOOR. On error the claim is released rather than
// latched, so a later gesture can try again: the alternative is a session
// permanently convinced it has seen the beginning because one RPC timed out.
func (in *interactiveInput) pagerCatchUp() {
	if in.caughtUp {
		return
	}
	in.caughtUp = true
	go in.readHistoryIntoPager()
}

func (in *interactiveInput) readHistoryIntoPager() {
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	r, rerr := in.fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(transcriptPageSize))
	rcancel()
	in.mu.Lock()
	defer in.mu.Unlock()
	if rerr != nil {
		in.caughtUp = false
		return
	}
	// The same fold, in the same order, as the deliberate door: apply carries the
	// open suffix, and setMoreBefore records what the WIRE said about the
	// beginning. Applying is safe here precisely because the pager is up: the
	// inline branch of OnClosed (which freezes to native scrollback) is not
	// reachable while tr.active, which is the trap this whole design is built
	// around.
	in.lt.apply(r)
	in.lt.setMoreBefore(r.More.Before)
	// The window was derived from the tail before this page existed; re-derive it
	// so the rows that just arrived are reachable by a scroll.
	in.lt.invalidateTranscriptWindow()
	in.lt.render()
}

// pageTranscript keeps the retained window fed. It never blocks the caller: the
// input loop calls it after every key and wheel event, and a scroll-up that
// needs older history must not stop the pager from drawing the frames the user
// is already scrolling through. One fetch runs at a time (pageInFlight); when
// it lands, the worker re-asks the pager whether the viewport has since moved
// close enough to another edge and chains straight into the next page, so a
// fast scroll pulls history continuously instead of one keypress at a time.
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
	if in.pageInFlight { // the running fetch re-checks the cursor when it lands
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

// prefetchTranscriptPages fetches req and then keeps going while the pager still
// wants a page in the direction it is being scrolled.
func (in *interactiveInput) prefetchTranscriptPages(req transcriptPageRequest, done chan struct{}) {
	defer close(done)
	for {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		if req.fill != nil {
			// A HOLE, not history below the floor: Ensure keeps reading until
			// the hole is closed, and merges into the store itself.
			err = in.lt.fillGap(rctx, *req.fill)
		}
		var messages historyPage
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

		// A hole inside the window is closed by Ensure; a page below the floor
		// is a plain backward read. The search walks both, because the reader
		// is going to be shown whatever it lands on.
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		var (
			messages historyPage
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

// ownTurnDeadline bounds the wait for our own question to come back on the
// stream. It is the answer to "how long before I conclude no inquiry is
// coming", and it is generous by three orders of magnitude on purpose.
//
// MEASURED, against a live agent (probe: issue Qua, timestamp the frame whose
// part carries our own text; 4 dials, warm aria):
//
//	Qua returned          +0.13 … +0.15 ms
//	our inquiry frame     +0.19 … +0.34 ms   (0.04-0.19 ms after Qua returned)
//
// which agrees with W1B's BenchmarkPromptBroadcastGap: the append→broadcast
// window inside the daemon is ~0.7µs on a 5,000-message aria and flat in aria
// size, so what we are timing is one socket round trip and one inbox drain.
// The whole cold path, exec to the question painted, is ~38ms.
//
// The wait is also only ever entered for an aria Qua reported IDLE (a running
// turn drops us into the pager instead), so the drain we are waiting on has
// nothing in front of it. 300ms is ~900× the slowest measurement.
//
// It has to exist because the other branch may deliver nothing for tens of
// seconds, a steer sits invisible behind a long tool call until the round
// boundary, and waiting on it would hang the prompt. It must not be tight,
// because being wrong is asymmetric: too short and a slow-but-ordinary prompt
// gets an unwanted preamble (the irritant this removes), too long and a turn
// we really did join waits a beat for its orientation. Nothing of OURS is
// delayed by it: the wait ends on the very frame that paints the question.
const ownTurnDeadline = 300 * time.Millisecond

// awaitOwnTurn reports whether the turn we are about to watch is the one our
// prompt opened. True means: our question came back on the wire, so the screen
// is about to show it and there is nothing to orient: print no preamble.
//
// An empty prompt has no inquiry to match and cannot be told apart this way;
// treat it as ours rather than orienting a session that asked nothing.
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

// pageCarriesInquiry reports whether a frame carries the question we sent.
//
// The inquiry travels as TEXT on the turn and is recorded verbatim
// (OpenInquiry(a.turnID, prompt.text)), so equality on the text is the whole
// test: no turn id arithmetic, which would need the cursor from Qua and the
// inquiry frame can beat Qua's own response back to us.
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

// recentContext reads the tail of the conversation for the opening preamble.
//
// Nothing here reaches the aria.Client: applying a history page would fire
// OnClosed for every historical message, and the inline branch of OnClosed
// Freezes to native scrollback: i.e. the retained history would be re-dumped
// into the terminal on every prompt. Folding the page separately
// (committedMessages, the same fold the pager's page fetch uses) gives the
// renderer the messages with no side effects at all.
//
// Two filters, both about not showing this session's own turn as its own
// history: SEALED parts only, so a turn another client is mid-way through is
// never half-printed; and nothing past the cursor Qua reported, which is the
// last committed turn at accept time. An error, a timeout, or an aria with no
// history all return nil: the caller then behaves exactly as before.
func recentContext(ctx context.Context, fcli transcriptReadClient, cursor int) historyPage {
	rctx, rcancel := context.WithTimeout(ctx, recentContextTimeout)
	defer rcancel()
	r, err := fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(recentContextMessages))
	if err != nil {
		return historyPage{}
	}
	var history aria.Page
	for _, part := range r.Parts {
		if part.Sealed && int(part.ID) <= cursor {
			history.Parts = append(history.Parts, part)
		}
	}
	return committedPage(history)
}

// wireBudget converts the pager's message-count geometry into the wire's byte
// budget. They are different units and conflating them is a bug: the client
// counts messages to size its retained window, while the wire spends bytes so
// that one enormous tool dump cannot blow a page. Passing a raw count (30)
// asked the server for 30 BYTES, and the paginator's "always emit at least one
// node" floor turned every page into a single node.
//
// Over-estimating is safe: the client keeps only what it asked for and the
// server clamps to page_budget_max. Under-estimating truncates.
const wireBytesPerMessage = 4096

func wireBudget(messages int) int {
	if messages <= 0 {
		return 0 // let the server apply its configured default
	}
	return messages * wireBytesPerMessage
}

// historyFetcher is the reader Store.Ensure closes holes with: the client's
// own ReadBefore, folded through the same committedPage the scroll-up path
// uses. One wire call, one fold, both directions of the design agreeing about
// what a page IS.
func (in *interactiveInput) historyFetcher() aria.Fetcher {
	return func(ctx context.Context, before aria.Anchor, limit int) (aria.Fetched, error) {
		r, err := in.fcli.ReadBefore(ctx, before, wireBudget(limit))
		if err != nil {
			return aria.Fetched{}, err
		}
		p := committedPage(r)
		return aria.Fetched{Msgs: p.msgs, Extents: p.extents, More: p.more}, nil
	}
}

func (in *interactiveInput) readTranscriptPage(ctx context.Context, req transcriptPageRequest) (historyPage, error) {
	limit := req.limit
	if limit <= 0 {
		limit = transcriptPageSize
	}
	at := aria.Anchor{Turn: uint64(req.before), Node: uint64(req.beforeNode)}
	r, err := in.fcli.ReadBefore(ctx, at, wireBudget(limit))
	if err != nil {
		return historyPage{}, err
	}
	return committedPage(r), nil
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

// refreshQueued kicks a background fetch of the aria's queued user prompts and
// writes the result into the transcript panel. Called from the transcript when
// the queued panel opens; safe to invoke concurrently with any prior fetch -
// the last completion wins (there is no ordering constraint on a purely
// observational panel).
func (in *interactiveInput) refreshQueued() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := in.fcli.Queued(ctx)
		in.mu.Lock()
		defer in.mu.Unlock()
		// No transcriptActive() gate: the queue is shown in BOTH views now, so
		// bailing unless the pager was up is exactly the bug that made a
		// waiting prompt invisible inline.
		if err != nil {
			in.lt.setTranscriptQueued(nil, err.Error())
			in.lt.render()
			return
		}
		texts := make([]string, 0, len(resp.Prompts))
		for _, p := range resp.Prompts {
			texts = append(texts, p.Text)
		}
		in.lt.setTranscriptQueued(texts, "")
		in.lt.render()
	}()
}

// run reads input until stdin errors, Ctrl-C (cancel), or Ctrl-D (disconnect).
// Call under a MakeRaw session so Ctrl-C/Ctrl-D arrive as bytes.
//
// One read is one frame. In raw mode a read returns everything the tty has
// buffered, so a mouse-wheel flick, and everything that piled up while the
// previous frame was being drawn, arrives as a single chunk. Bracketing the
// whole chunk in a transcript batch collapses it into one paint at the settled
// offset, instead of one paint per event with only the last one wanted. The
// buffer is sized to hold a whole flick (a wheel report is ~6 bytes) rather
// than chopping it into frames.
func (in *interactiveInput) run() {
	defer in.cancelSelectionCopy()
	defer in.cancelTranscriptSearch()
	buf := make([]byte, 4096)
	var pending []byte // a mouse/escape sequence split across reads
	for {
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

// consume dispatches one chunk of input as a single frame. It returns any
// trailing bytes of an escape sequence split across reads, and whether the
// input loop should stop.
func (in *interactiveInput) consume(data []byte) (pending []byte, stop bool) {
	in.mu.Lock()
	in.lt.beginTranscriptBatch()
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		in.lt.endTranscriptBatch() // the settled state, painted once
		in.mu.Unlock()
		if in.pageWanted {
			in.pageWanted = false
			in.pageTranscript() // after the frame: it may block on an RPC
		}
	}()
	i := 0
	for i < len(data) {
		in.mu.Lock()
		mode := in.lt.transcriptMode()
		// Mouse sequences are only expected when the PAGER is up: that is where
		// mouse reporting is enabled. Ask the pager, not the mode: a non-incipit
		// mode can be live without the pager being up, and letting ldmouse.Parse
		// see input then swallows a bare Esc as a possible mouse prefix.
		active := in.lt.transcriptActive()
		in.mu.Unlock()
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
					// PRESS ONLY. The terminal reports the release of the same click
					// too, and a toggle gesture that fired on both would flip twice per
					// click and so appear never to fire at all.
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
		// Decode: bytes and escape encodings become one logical chord. This
		// is the only part of the loop that knows about terminals; from the
		// keyEvent on, the keymap decides everything (see keymap.go).
		var ev keyEvent
		if key, consumed, ok, need := parseModifiedKey(data[i:]); need {
			pending = append(pending, data[i:]...)
			break
		} else if ok {
			i += consumed
			if letter, isCtrl := ctrlChordLetter(key); isCtrl {
				// A Ctrl+letter the table binds as a CSI-u chord, modifiers
				// intact (Shift/Alt extend the node selection). Every other
				// CSI-u key reduces to the byte it would have arrived as.
				ev = keyEvent{ctrl: letter, shift: key.shift, alt: key.alt, mode: mode}
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
			// A leading ESC we didn't recognize as CSI-u. Delimit the rest of
			// the sequence (arrow keys, SS3 F-keys, OSC replies, generic CSI)
			// so bare Esc can trigger its own binding without a sequence
			// prefix spuriously firing it.
			if ec, en := consumeEscapeSequence(data[i:]); en {
				pending = append(pending, data[i:]...)
				break
			} else if ec > 0 {
				seq := data[i : i+ec]
				i += ec
				in.lastNL = 0
				// Delimited, now classified: the arrow cluster drives the
				// pager. Everything else stays swallowed whole.
				key, ok := navKeyFor(seq)
				if !ok {
					continue
				}
				ev = keyEvent{nav: key.nav, shift: key.shift, alt: key.alt, mode: mode}
			} else {
				// Bare Esc: a key in its own right.
				b := data[i]
				i++
				if in.coalesceNewline(b) {
					continue
				}
				ev = keyEvent{b: b, mode: mode}
			}
		} else {
			// Coalesce CR+LF (and the mirrored LF+CR) into ONE newline event.
			// Windows conhost, and some other terminals: emit both bytes for
			// a single Enter keypress; without this dedup a toggle binding on
			// Enter fires twice per press and appears stuck. parseModifiedKey
			// handles the CSI-u path (Enter as code 13); this raw-byte
			// fallback is the one that sees non-CSI-u terminals.
			b := data[i]
			i++
			if in.coalesceNewline(b) {
				continue
			}
			ev = keyEvent{b: b, mode: mode}
		}
		// A key whose pager meaning is a sensible OPENING gesture yanks the
		// pager up first, so it acts on arrival instead of looking like a dead
		// keyboard. Which keys those are is one field on one table row.
		if ev.mode == modeIncipit && opensTranscript(ev) {
			in.enterTranscript()
			in.mu.Lock()
			ev.mode = in.lt.transcriptMode()
			in.mu.Unlock()
		}
		// Input-level rows first: the keys that own the process (interrupt,
		// detach, listen, clipboard) rather than the viewport.
		if act := inputAct.input(ev.mode, ev); act != nil {
			if act(in, ev) == keyStop {
				return pending, true
			}
			continue
		}
		// Nothing owns it up here: hand it to the pager, a motion, a panel, or
		// literal text in the search box.
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

// ---------------------------------------------------------------------------
// The input loop's key actions: the rows of the keymap that own the process
// rather than the viewport (see keymap.go). They run on the read goroutine,
// take the render lock themselves, and may end the loop.
// ---------------------------------------------------------------------------

// inputInterrupt is Ctrl-C. It is a state machine over the selection and the
// clipboard, not a single gesture, and the order matters:
//
//	a copy already failed on this exact selection → clear it and quit
//	a copy is in flight                          → cancel it, remember the failure
//	a selection is up                            → start an async copy
//	nothing selected                             → interrupt the turn and close
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

	// CTRL-C STOPS THE TURN AND THEN EXITS WHEN IT STOPS.
	//
	// IT USED TO JUST LEAVE. `in.cancel()` cancels the CLIENT's context and
	// keyStop quits the process; the daemon was never told, so the turn ran
	// on with nobody watching. The help said otherwise --
	// "Ctrl-C  Interrupt the turn (sends figaro.interrupt)" (cli.go:367) --
	// and a grep for MethodInterrupt across internal/cli found exactly one
	// hit, a method-name list in replay.go. NOTHING IN THE CLIENT EVER SENT
	// IT. Every unit test passed because they call Agent.Interrupt directly,
	// which is the half that works.
	//
	// The exit is not ours to perform: turn.done with idle already closes
	// doneCh in incipit (see onNotify), so stopping the turn IS the exit.
	// Firing the RPC and quitting immediately would race that and abandon the
	// turn again, differently.
	//
	// THE SECOND PRESS IS AN ESCAPE HATCH, and it must exist: if the daemon is
	// wedged or the RPC never lands, a user who presses Ctrl-C must still be
	// able to leave. Press once to stop the turn, twice to go now.
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
	in.lt.report("interrupting; leaving when the turn stops (Ctrl-C again to leave now)")
	in.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := in.hangup.Hangup(ctx, rpc.QueueKeep); err != nil {
			// The turn will not be stopping, so nothing will close doneCh for
			// us. Leave rather than hang, and say why.
			in.mu.Lock()
			in.lt.report("interrupt failed: " + err.Error())
			in.mu.Unlock()
			in.cancel()
		}
	}()
	return keyHandled
}

// inputHangUp is 'H' in the pager: stop the turn, and STAY.
//
// It is the third thing you can do to a running turn, and the one that was
// missing. Ctrl-C stops the turn and takes the session with it; Ctrl-D leaves
// the session and lets the turn run on; this stops the turn and keeps the
// session: which is what you want when the model is off down the wrong path
// and you intend to say so in a moment.
//
// The queue is KEPT (the same disposition `figaro hup` sends): whatever you
// typed while it was working coalesces into one message and is answered next.
// `figaro cut` is the other choice, and it is a separate deliberate act rather
// than a modifier on this key: losing a queue must never be one keystroke
// away from stopping a turn.
//
// The RPC runs off the input goroutine: the socket round trip must not freeze
// the pager, and the reader gets an immediate notice either way. The turn's
// own end arrives as turn.done, which paints the status token as usual.
func inputHangUp(in *interactiveInput, _ keyEvent) keyVerdict {
	return in.hangUp(rpc.QueueKeep)
}

// inputHangUpDrop is 'X': the same, and the queue goes with it. What was
// dropped is reported rather than swallowed: into the status notice, and
// through the pending report, which leaveTranscript reprints to the shell. So
// the text survives even though its place in the queue does not.
func inputHangUpDrop(in *interactiveInput, _ keyEvent) keyVerdict {
	return in.hangUp(rpc.QueueClear)
}

func (in *interactiveInput) hangUp(disposition rpc.QueueDisposition) keyVerdict {
	if in.hangup == nil {
		in.mu.Lock()
		in.lt.report("hang up: not connected")
		in.mu.Unlock()
		return keyHandled
	}
	in.mu.Lock()
	if disposition == rpc.QueueClear {
		in.lt.report("hanging up: dropping the queue")
	} else {
		in.lt.report("hanging up: staying attached")
	}
	in.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := in.hangup.Hangup(ctx, disposition)
		in.mu.Lock()
		defer in.mu.Unlock()
		switch {
		case err != nil:
			in.lt.report("hang up failed: " + err.Error())
		case resp.Cleared && len(resp.Queue) > 0:
			// ONE report, with the list inside it. report() folds newlines for
			// the status row (which is one physical line) and keeps the raw
			// text for the pending report, which leaveTranscript reprints to
			// the shell: so the summary is readable live and the full list
			// lands in scrollback. N separate reports would instead leave the
			// status row showing only the LAST message, which is how this read
			// in the pty: a lone "doomed two" with no header.
			var b strings.Builder
			fmt.Fprintf(&b, "hung up: listening; dropped %s:", queueCount(resp.Queue))
			for _, p := range resp.Queue {
				b.WriteString("\n  " + queueRowText(p.Text))
			}
			in.lt.report(b.String())
		case resp.Cleared:
			in.lt.report("hung up: listening (nothing was queued)")
		case len(resp.Queue) > 0:
			// Say what survived, because the whole point of keeping it is that
			// it is about to be asked.
			in.lt.report(fmt.Sprintf("hung up: listening (%s queued, answered next)",
				queueCount(resp.Queue)))
		default:
			in.lt.report("hung up: listening")
		}
	}()
	return keyHandled
}

// inputDisconnect is Ctrl-D, and 'q' with the pager up: leave, and let the
// turn keep running.
func inputDisconnect(in *interactiveInput, _ keyEvent) keyVerdict {
	in.cancelTranscriptSearch()
	in.cancelSelectionCopy()
	select {
	case in.disconnectCh <- struct{}{}:
	default:
	}
	return keyStop
}

// inputEnterTranscript is Ctrl-T / Ctrl-L: open the transcript pager. Entering
// it: by any route: carries listen semantics: the session stays open until an
// explicit q / Ctrl-D / Ctrl-C.
func inputEnterTranscript(in *interactiveInput, _ keyEvent) keyVerdict {
	in.cancelTranscriptSearch()
	in.enterTranscript()
	return keyHandled
}

// inputToggleVerbose is Ctrl-O.
func inputToggleVerbose(in *interactiveInput, _ keyEvent) keyVerdict {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	in.set.verbose = !in.set.verbose
	in.lt.invalidateTranscriptRows()
	in.lt.render()
	in.mu.Unlock()
	return keyHandled
}

// inputYank is 'y': copy the selection if there is one, else the aria id.
func inputYank(in *interactiveInput, ev keyEvent) keyVerdict {
	if ev.mode != modeIncipit {
		in.mu.Lock()
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
	in.tc.SetClipboard(in.figaroID)
	return keyHandled
}

// inputSelectNext/Prev are CSI-u Ctrl-N / Ctrl-P: move the node selection, or
// extend it when a modifier rides along. They are input-level (not pager
// rows) because only the CSI-u report carries the modifier at all; the raw
// 0x0e/0x10 bytes go through the pager, which cannot see one.
func inputSelectNext(in *interactiveInput, ev keyEvent) keyVerdict {
	return in.selectNodeKey(1, ev)
}

func inputSelectPrev(in *interactiveInput, ev keyEvent) keyVerdict {
	return in.selectNodeKey(-1, ev)
}

func (in *interactiveInput) selectNodeKey(delta int, ev keyEvent) keyVerdict {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	in.lt.transcriptSelect(delta, ev.shift || ev.alt)
	in.mu.Unlock()
	in.pageWanted = true
	return keyHandled
}

// clickTranscript is a left-button press in the pager: select the node under the
// pointer, or toggle its expansion when it is already the focus.
//
// Clickability is asked FIRST and the whole gesture is skipped when the answer
// is no. A click is the one input that can miss, a keystroke is always aimed,
// a pointer lands where it lands: so a click on a blank row between two nodes
// must not cancel a search prompt, dismiss a panel, or cost a frame.
func (in *interactiveInput) clickTranscript(ev ldmouse.Event) {
	row := ev.Y - 1 // the report is 1-based; screen rows are 0-based
	acted := false
	in.mu.Lock()
	if in.lt.transcriptClickable(row) {
		in.cancelTranscriptSearchLocked()
		acted = in.lt.transcriptClick(row, ev.Shift())
	}
	in.mu.Unlock()
	if acted {
		// Same reason ^N sets it: a selection near the floor of the retained window
		// can want older history, and the fetch must run off the render lock.
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

// coalesceNewline swallows the paired byte of a CR+LF (or LF+CR) sequence so
// a single Enter keypress fires the pager binding exactly once. Windows
// conhost is the canonical offender; some serial-console setups too. Returns
// true when the current byte is the second half of a pair (and thus should
// be skipped by the input loop).
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

// dimRule returns a plain dim full-width horizontal rule: the opening rule and
// the closer after a non-assistant (user/steering) message.
func dimRule() string { return term.Dim(strings.Repeat("─", termWidth())) }

// sessionLine writes a line with an explicit CR: a painted session has
// DISABLE_NEWLINE_AUTO_RETURN armed on Windows, where a bare LF staircases
// (microsoft/WSL#1273).
func sessionLine(w io.Writer, s string) {
	fmt.Fprint(w, s+"\r\n")
}

// endSession is the last write: the shell prompts where we leave the cursor,
// so return the carriage. A lone CR adds no row.
func endSession(w io.Writer) {
	fmt.Fprint(w, "\r"+cursorShow+autowrapOn)
}

// termWidth returns the terminal width, defaulting to 80.
func termWidth() int {
	if w := term.Width(); w > 0 {
		return w
	}
	return 80
}

// queuedPollHz is how often the accepted-but-unplaced queue is re-read. See
// the ticker in the stream loop for why it is a poll and not a subscription.
const queuedPollHz = 2
