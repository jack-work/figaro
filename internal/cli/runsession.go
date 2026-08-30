package cli

// ONE SESSION, TWO ENTRANCES.
//
// `figaro send` and `figaro listen` were two implementations of the same
// thing: build the livelog turn, arm autowrap and hide the cursor, defer the
// teardown, print the opening rule, take the render lock, start the pager
// clock, register a resize handler, MakeRaw and run the input loop, then
// select on the ways a session ends. They differed by accident far more than
// by design, and every one of those accidents shipped:
//
//   - the queue poll lived only in send's ticker, so a `listen` pager never
//     refreshed its queue pit;
//   - metrics ride reads and frames, and listen's cold path did neither, so
//     the bar's capacity figure came and went;
//   - listen never reported WHY a turn failed;
//   - the hooks were wired at three call sites, and the file already carried
//     the scar: "a hook that is armed on one of two doors is armed on neither".
//
// So there is one body. What actually differs is stated as data:
//
//	send    a prompt to send once the session is up, and an exit when the
//	        turn settles (unless the pager is up: then it has listen semantics)
//	listen  no prompt; it follows until the reader leaves
//
// Everything else -- the tape, the form pit, a replay's end channel, the
// angelus door that `:open` needs -- is a field.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk"
)

// sessionOpts is what the two entrances differ by.
type sessionOpts struct {
	figaroID string
	ep       transport.Endpoint
	loaded   *config.Loaded
	set      renderSettings

	// prompt makes this a SEND: it is submitted once the session is up, and
	// the session ends when the turn settles.
	prompt string

	// acli is the angelus door `:open` and `:attend` need; nil leaves the
	// subject-switching verbs inert, which is what a replay wants.
	acli *sdk.Angelus
	// tape records the wire (nil records nothing).
	tape *tape.Writer
	// end closes when the caller's stream is over -- the end of a replayed
	// tape -- and joins the same select the turn-over path uses.
	end <-chan struct{}
	// startedAt overrides the session clock: a replay passes the recording's
	// own start so the same tape paints the same pixels at any hour.
	startedAt time.Time
	// formPit opens the subject's form in the pit, fullscreen, and reads no
	// history at all. It is the whole of `fig form listen`.
	formPit bool
	// ownsSubject says whether this loop may close the connection it holds.
	ownsSubject bool
	// signals wraps the context so SIGINT interrupts the turn. Only a session
	// that did not already arrange that asks for it.
	signals bool
}

// runSession is the whole of a live TTY session, for both entrances.
func runSession(ctx context.Context, cancel context.CancelFunc, opt sessionOpts) {
	startedAt := opt.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	status := newSessionStatus(opt.figaroID, startedAt)

	if opt.signals {
		var sigCancel context.CancelFunc
		ctx, sigCancel = signal.NotifyContext(ctx, os.Interrupt)
		defer sigCancel()
	}

	width, height := term.Width(), term.Height()
	if width <= 0 {
		width = 80
	}
	// The bookend is this session's status line, gated on the config. IT READS
	// THE LIVE STATUS, not the one this function happened to build: retarget
	// swaps in a fresh sessionStatus (every `:open` does), and a closure over
	// the original painted a bookend nobody was updating -- no state glyph, no
	// mantra, no capacity figure, in the one entrance that shows a bookend.
	var lt *livelogTurn
	var bookendFn func() []string
	if opt.loaded.StatusLine() {
		bookendFn = func() []string { return bookendLines(lt.status) }
	}
	set := opt.set
	lt = newLivelogTurn(os.Stdout, width, height, &set, opt.figaroID, startedAt, status, bookendFn, dimRule)
	tc := term.NewClient()

	// The renderer owns the cursor and assumes one row per line: no auto-margin
	// (a full-width row must not wrap), no cursor. It draws in incipit, so
	// frozen output lands in the normal scrollback.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer endSession(os.Stdout)
	// Deferred right-edge wrap belongs to the painter, not to every command
	// that prints a line. OnceFunc because the defer and exitNow both fire.
	restoreWrap := sync.OnceFunc(term.ArmDeferredWrap())
	defer restoreWrap()
	atExit(restoreWrap)
	defer lt.leaveTranscript()
	lt.openRule() // the renderer owns the rule AND the margin under it

	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1) // Ctrl-D: leave the turn running

	// THE DEFERS ABOVE DO NOT RUN ON os.Exit, and two exits here are: die()
	// and the Ctrl-C 130 path. Both would leave the terminal on the alternate
	// screen with the cursor hidden.
	atExit(func() {
		// TryLock: the hook must never be the reason a dying process hangs.
		if mu.TryLock() {
			defer mu.Unlock()
		}
		lt.leaveTranscript()
		endSession(os.Stdout)
	})

	lt.setRenderLock(&mu)
	// FREEZE FORENSICS: the watchdog dumps every goroutine when this lock has
	// been unavailable for seconds; SIGUSR1/SIGUSR2 do it on demand. A pager
	// that has stopped answering cannot be asked what it is doing.
	defer watchRenderLock(&mu)()
	defer armFreezeSignals()()

	// Hold frames until the opening preamble is placed: a subscription pushes
	// the moment it is made, and the question must be painted UNDER the
	// history it follows. Nothing is dropped.
	if opt.prompt != "" {
		lt.holdFrames()
	}

	in := &interactiveInput{
		tc: tc, lt: lt, mu: &mu, set: &set,
		figaroID: opt.figaroID, cancel: cancel, disconnectCh: disconnectCh,
		acli: opt.acli, loaded: opt.loaded, tap: tapeTap(opt.tape),
		ownsSubject: opt.ownsSubject, subjectDead: make(chan struct{}, 1),
		// The send half: the prompt this session is waiting on, and the
		// channels that say whether a turn of ours ever opened.
		prompt: opt.prompt, sendCursor: -1, doneCh: doneCh,
		watchInquiry: opt.prompt != "",
		ownTurn:      make(chan struct{}), noTurn: make(chan struct{}),
		// An inline send stays inline until something promotes it; every other
		// session opens the pager as it starts.
		startInline: opt.prompt != "" && !set.listen,
	}
	defer func() {
		if in.ownsSubject && in.subject != nil {
			in.subject.Close()
		}
	}()

	in.lt.setCatchUp(in.pagerCatchUp)
	// THE SUBJECT COMES IN THROUGH THE SAME DOOR IT LATER SWITCHES THROUGH:
	// retarget dials, wires the pump, the hooks and the desync handler, and
	// seeds. `:open` calls exactly the same function, so the startup path is
	// the switch path.
	if err := in.retarget(ctx, opt.figaroID, opt.ep); err != nil {
		die("%s", err)
	}

	defer startPagerClock(&mu, lt, func() *interactiveInput { return in })()

	defer tc.OnResize(func(w, h int) {
		mu.Lock()
		lt.resize(w, h)
		mu.Unlock()
	})()

	// MakeRaw so Ctrl-C/Ctrl-D arrive as input bytes -- portable, and identical
	// in incipit and transcript: the input loop owns them, not a signal.
	if tc.IsTTY() {
		if restore, err := tc.MakeRaw(); err == nil {
			// The restore must survive os.Exit, not only a normal return:
			// measured, Ctrl-C during a turn left the shell in raw mode.
			restoreOnce := sync.OnceFunc(restore)
			defer restoreOnce()
			atExit(restoreOnce)
			fmt.Fprint(os.Stdout, enableModifiedKeyReporting)
			defer fmt.Fprint(os.Stdout, disableModifiedKeyReporting)
			// Belt and braces: a crash mid-pager must not leave the shell
			// spewing raw \x1b[<…M.
			defer os.Stdout.WriteString(ldmouse.Disable)
			if opt.formPit {
				// The form, and only the form: no history is fetched, and a
				// fullscreen pit obscures what has not been fetched. Off this
				// goroutine, because entering takes the render lock.
				go func() {
					in.enterFormPager()
					in.openLive("form show", opt.figaroID, true)
				}()
			} else if set.listen && opt.prompt != "" {
				in.enterTranscript() // --listen: open the pager immediately
			}
			go in.run()
		} else {
			fmt.Fprintf(os.Stderr, "figaro: terminal input disabled: enter raw mode: %v\n", err)
		}
	}

	if opt.prompt != "" {
		in.sendPrompt(ctx)
	}

	locked := func(fn func()) {
		// EVERY RENDERER ENTRY POINT UNDER mu, INCLUDING THE LAST ONE: the
		// clock, the notify pump and the pacer's trailing render are all still
		// live here, stopped by defers that have not fired yet.
		mu.Lock()
		defer mu.Unlock()
		fn()
	}
	select {
	case <-doneCh:
		// The committed bookend is the final line; nothing more to print.
	case <-opt.end:
		locked(func() { lt.finishTurn("") }) // the tape ran out
	case <-disconnectCh:
		// q / Ctrl-D. A turn that already finished while the pager was up is a
		// clean exit, not an abandonment: the completed tail reaches
		// scrollback intact.
		locked(func() { lt.abandon(turnStatusDisconnected) })
	case <-in.subjectDead:
		locked(func() { lt.abandon(turnStatusError) })
		if opt.prompt != "" {
			os.Exit(1)
		}
	case <-ctx.Done():
		in.interruptAndLeave(&mu, doneCh)
	}
}

// sendPrompt is the half of a session that only a send has: submit the prompt,
// decide whether we joined a turn already in flight, and place the preamble.
func (in *interactiveInput) sendPrompt(ctx context.Context) {
	// The opening preamble is for the INLINE renderer only: --listen already
	// opened the pager (which reads its own history), and a non-TTY caller
	// gets a stream, not a screen.
	catchUp := in.tc.IsTTY() && !in.set.listen

	cursor, active, qerr := in.subject.Qua(ctx, in.prompt, buildPromptForm())
	if qerr != nil {
		dieWithClosure(qerr, "prompt: %s", qerr)
	}
	in.mu.Lock()
	in.sendCursor = cursor
	in.lt.status.beginTurn()
	in.mu.Unlock()

	// Joining a turn already running: the inline renderer cannot cleanly paint
	// a turn mid-stream, so drop into the pager on the last page.
	joined := active && in.tc.IsTTY()
	if joined {
		in.enterTranscript()
	}

	var fetched historyPage
	if catchUp && !joined && !awaitOwnTurn(in.prompt, in.ownTurn, in.noTurn) {
		fetched = recentContext(ctx, in.subject, cursor)
	}
	in.mu.Lock()
	in.watchInquiry = false // decided; no frame need ask again
	in.lt.openInline(fetched)
	in.mu.Unlock()
}

// interruptAndLeave is Ctrl-C at the session level: stop the turn in flight,
// wait briefly for it to land, and exit 130 if there was one. With nothing
// running it is a clean close.
func (in *interactiveInput) interruptAndLeave(mu *sync.Mutex, doneCh <-chan struct{}) {
	mu.Lock()
	wasRunning := in.lt.status.turnRunning()
	mu.Unlock()
	if !wasRunning {
		return
	}
	mu.Lock()
	in.lt.report("interrupting")
	mu.Unlock()

	intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if cli := in.aria(); cli != nil {
		_ = cli.Interrupt(intCtx)
	}
	intCancel()
	select {
	case <-doneCh:
	case <-in.subjectDead:
	case <-time.After(3 * time.Second):
		mu.Lock()
		in.lt.abandon(turnStatusInterrupted)
		mu.Unlock()
	}
	mu.Lock()
	in.lt.report("interrupted")
	mu.Unlock()
	if code := interruptExit(true); code != 0 {
		exitNow(code) // hooks first: the pager may still be up
	}
}

// turnFrame is the notify pump's frame half, shared by both entrances: fold
// the page, and answer the one question a send asks of the first frames --
// did a turn of OURS open?
func (in *interactiveInput) turnFrame(params json.RawMessage) {
	var r aria.Page
	if json.Unmarshal(params, &r) != nil {
		return
	}
	if in.watchInquiry && pageCarriesInquiry(r, in.prompt) {
		in.watchInquiry = false
		in.ownOnce.Do(func() { close(in.ownTurn) })
	}
	in.lt.apply(r)
}

// turnDone is the notify pump's other half. THE REPORT IS FOR BOTH
// ENTRANCES -- a `listen` pager used to swallow the reason a turn failed, so
// an auth failure was a bare ✗ in neither the bar nor the log -- and the
// SETTLING is only a send's, because a listener has nothing to settle.
func (in *interactiveInput) turnDone(params json.RawMessage) {
	var d rpc.DoneEntry
	_ = json.Unmarshal(params, &d)

	// Tear the live region down FIRST, so a hint lands on clean scrollback
	// below it rather than over the footer.
	in.lt.finishTurn(d.Reason)
	if strings.HasPrefix(d.Reason, "error:") {
		in.noOnce.Do(func() { close(in.noTurn) })
		if hint, ok := authFailureHint(d.Reason); ok {
			in.lt.report(hint)
		} else {
			in.lt.report(d.Reason)
		}
	}
	if in.prompt == "" {
		return
	}
	// Settle when the agent reports idle: a turn that ended with our steer
	// still queued reports idle=false, so we wait for our own. A daemon
	// predating the field sends nil, which is the pre-steering behaviour.
	// Never gate on the cursor advancing: the final commit can arrive via
	// async desync recovery AFTER this one-shot turn.done.
	idle := d.Idle == nil || *d.Idle
	if in.sendCursor < 0 || !idle {
		return
	}
	// Turn-done closes the session only in incipit: once the pager is up,
	// however it was entered, it has listen semantics.
	if in.lt.inTranscript() {
		return
	}
	select {
	case in.doneCh <- struct{}{}:
	default:
	}
}
