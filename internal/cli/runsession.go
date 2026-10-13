package cli

// One session body for send and listen: a send submits a prompt and exits when the
// turn settles, unless the pager is up, in which case it has listen semantics.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/internal/mark"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/figaro/sdk/aria"
)

type sessionOpts struct {
	figaroID string
	ep       transport.Endpoint
	loaded   *config.Loaded
	set      renderSettings

	prompt string   // non-empty makes this a send
	dress  dressing // rides the submission, so two sends in flight cannot wear each other's

	acli          *sdk.Angelus    // nil leaves the subject-switching verbs inert (a replay)
	tape          *tape.Writer    // nil records nothing
	end           <-chan struct{} // the caller's own end of stream (a replayed tape)
	startedAt     time.Time       // a replay passes the recording's start
	formPit       bool            // open the form in the pit fullscreen; reads no history
	formIntrinsic string
	ownsSubject   bool // may close the connection it holds
	signals       bool // wrap the context so SIGINT interrupts the turn
}

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
	// The bookend reads the live status through lt: retarget swaps in a fresh
	// sessionStatus.
	var lt *livelogTurn
	var bookendFn func() []string
	if opt.loaded.StatusLine() {
		bookendFn = func() []string { return bookendLines(lt.status) }
	}
	set := opt.set
	lt = newLivelogTurn(os.Stdout, width, height, &set, opt.figaroID, startedAt, status, bookendFn, dimRule)
	defer teeSlogInto(lt.tr.notes)()
	if in := opt.loaded; in != nil {
		lt.tr.setNoticeTTL(in.NoticeTTL())
	}
	tc := term.NewClient()

	// The renderer assumes one row per line: a full-width row must not wrap.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer endSession(os.Stdout)
	// OnceFunc: the defer and the exit hook both fire.
	restoreWrap := sync.OnceFunc(term.ArmDeferredWrap())
	defer restoreWrap()
	atExit(restoreWrap)
	defer lt.leaveTranscript()
	lt.openRule()

	var mu renderLock
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1) // Ctrl-D: leave the turn running

	// The defers above do not run on exit; without this the shell is left on
	// the alt screen with the cursor hidden.
	atExit(func() {
		// TryLock: the hook must never be the reason a dying process hangs.
		if mu.TryLock() {
			defer mu.Unlock()
		}
		lt.leaveTranscript()
		endSession(os.Stdout)
	})

	lt.setRenderLock(&mu)
	// The watchdog dumps every goroutine when the render lock is held for
	// seconds; SIGUSR1/SIGUSR2 on demand.
	defer watchRenderLock(&mu)()
	defer armFreezeSignals()()
	defer markMemory(&mu, lt, time.Second)()

	// A subscription pushes the moment it is made; the question must paint
	// under the history it follows.
	if opt.prompt != "" {
		lt.holdFrames()
	}

	in := &interactiveInput{
		tc: tc, lt: lt, mu: &mu, set: &set,
		figaroID: opt.figaroID, cancel: cancel, disconnectCh: disconnectCh,
		acli: opt.acli, loaded: opt.loaded, tap: tapeTap(opt.tape),
		ownsSubject: opt.ownsSubject, subjectDead: make(chan struct{}, 1),
		prompt: opt.prompt, dress: opt.dress, sendCursor: -1, doneCh: doneCh,
		watchInquiry: opt.prompt != "",
		ownTurn:      make(chan struct{}), noTurn: make(chan struct{}),
		startInline: opt.prompt != "" && !set.listen,
		intrinsics:  newIntrinsicMirrors(),
	}
	// The jumplist starts where the session does, or ^O has nowhere to go back to.
	in.jumps.visit(opt.figaroID)
	defer func() {
		if in.ownsSubject && in.subject != nil {
			in.subject.Close()
		}
	}()

	in.lt.setCatchUp(in.pagerCatchUp)
	// The startup path is the switch path: `:listen` calls the same retarget.
	if err := in.retarget(ctx, opt.figaroID, opt.ep); err != nil {
		die("%s", err)
	}

	defer startPagerClock(&mu, lt, func() *interactiveInput { return in })()

	defer tc.OnResize(func(w, h int) {
		mu.Lock()
		lt.resize(w, h)
		mu.Unlock()
	})()

	// Raw mode: Ctrl-C and Ctrl-D arrive as input bytes, owned by the input
	// loop, not a signal.
	if tc.IsTTY() {
		if restore, err := tc.MakeRaw(); err == nil {
			// The restore must survive an exit hook as well as a
			// normal return.
			restoreOnce := sync.OnceFunc(restore)
			defer restoreOnce()
			atExit(restoreOnce)
			fmt.Fprint(os.Stdout, enableModifiedKeyReporting+enableBracketedPaste)
			defer fmt.Fprint(os.Stdout, disableModifiedKeyReporting+disableBracketedPaste)
			// A crash mid-pager must not leave the shell spewing mouse
			// reports.
			defer os.Stdout.WriteString(ldmouse.Disable)
			// restore, not restoreOnce: the once is the teardown's,
			// and spending it here leaves the shell raw at exit.
			in.suspendTerm = func() {
				fmt.Fprint(os.Stdout, ldmouse.Disable+disableModifiedKeyReporting+disableBracketedPaste+altScreenOff+cursorShow+autowrapOn)
				restore()
			}
			in.resumeTerm = func() {
				if _, err := tc.MakeRaw(); err != nil {
					fmt.Fprintf(os.Stderr, "figaro: raw mode: %v\n", err)
				}
				fmt.Fprint(os.Stdout, autowrapOff+cursorHide+altScreenOn+ldmouse.Enable+enableModifiedKeyReporting+enableBracketedPaste)
			}
			if opt.formPit {
				// Off this goroutine: entering takes the render lock.
				go func() {
					in.enterFormPager()
					in.openLive("form show", formAddress(opt.figaroID, opt.formIntrinsic), true)
				}()
			} else if set.listen && opt.prompt != "" {
				in.enterTranscript()
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
		// The clock, the notify pump and the pacer are still live here:
		// every renderer entry stays under mu.
		mu.Lock()
		defer mu.Unlock()
		fn()
	}
	select {
	case <-doneCh:
		var failed bool
		locked(func() { failed = in.turnFailed })
		if failed {
			exitNow(exitTurnFailed) // through the hooks: the terminal is still raw
		}
	case <-opt.end:
		locked(func() { lt.finishTurn("") })
	case <-disconnectCh:
		// Leaving the pager does not make a failed turn of ours a success.
		var failed bool
		locked(func() { lt.abandon(turnStatusDisconnected); failed = in.turnFailed })
		if failed {
			exitNow(exitTurnFailed)
		}
	case <-in.subjectDead:
		locked(func() { lt.abandon(turnStatusError) })
		if opt.prompt != "" {
			exitNow(exitTurnFailed)
		}
	case <-ctx.Done():
		in.interruptAndLeave(&mu, doneCh)
	}
}

func (in *interactiveInput) sendPrompt(ctx context.Context) {
	// The preamble is the inline renderer's only: the pager reads its own history.
	catchUp := in.tc.IsTTY() && !in.set.listen

	qua := mark.Span("submit.qua", "to", in.figaroID, "len", len(in.prompt))
	cursor, active, qerr := in.subject.Qua(ctx, in.prompt, buildPromptForm(in.dress))
	qua("active", active, "err", qerr != nil)
	if qerr != nil {
		dieWithClosure(qerr, "prompt: %s", qerr)
	}
	in.mu.Lock()
	in.sendCursor = cursor
	in.lt.status.beginTurn()
	in.mu.Unlock()

	// The inline renderer cannot paint a turn mid-stream: join through the pager.
	joined := active && in.tc.IsTTY()
	if joined {
		in.enterTranscript()
	}

	var fetched aria.Page
	if catchUp && !joined && !awaitOwnTurn(in.prompt, in.ownTurn, in.noTurn) {
		fetched = recentContext(ctx, in.subject, cursor)
	}
	in.mu.Lock()
	in.watchInquiry = false
	in.lt.openInline(fetched)
	in.mu.Unlock()
}

// interruptAndLeave exits 130 only if a turn was running.
func (in *interactiveInput) interruptAndLeave(mu *renderLock, doneCh <-chan struct{}) {
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
		exitNow(code)
	}
}

func (in *interactiveInput) turnFrame(params json.RawMessage) {
	var r aria.Page
	if json.Unmarshal(params, &r) != nil {
		return
	}
	if in.watchInquiry && pageCarriesInquiry(r, in.prompt) {
		in.watchInquiry = false
		in.ownOnce.Do(func() { close(in.ownTurn) })
	}
	if mark.Enabled() {
		live, nodes := 0, 0
		for _, p := range r.Parts {
			if p.Live != nil {
				live++
				nodes += len(p.Live.Nodes)
			}
		}
		mark.Mark("aria.frame", "bytes", len(params), "parts", len(r.Parts), "live", live, "deltas", nodes)
	}
	in.lt.apply(r)
}

func (in *interactiveInput) turnDone(params json.RawMessage) {
	var d rpc.DoneEntry
	_ = json.Unmarshal(params, &d)

	// Report before finishTurn: it commits the bar to scrollback, so the
	// reason must already be on it.
	failed := strings.HasPrefix(d.Reason, "error:")
	if failed {
		in.noOnce.Do(func() { close(in.noTurn) })
		if hint, ok := authFailureHint(d.Reason); ok {
			in.lt.reportError(hint)
		} else {
			in.lt.reportError(d.Reason)
		}
		if in.prompt != "" && in.sendCursor >= 0 {
			in.turnFailed = true
		}
	}
	in.lt.finishTurn(d.Reason)
	if in.prompt == "" {
		return
	}
	// Settle on idle, not on the cursor advancing: the final commit can arrive
	// by desync recovery after this one-shot turn.done.
	idle := d.Idle == nil || *d.Idle
	if in.sendCursor < 0 || !idle {
		return
	}
	if in.lt.inTranscript() {
		return
	}
	select {
	case in.doneCh <- struct{}{}:
	default:
	}
}
