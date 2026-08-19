package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
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

// runListen tails an aria with the same renderer the rich send uses,
// minus the Qua call: it catches up to the committed cursor, follows
// live frames, supports Ctrl-T transcript mode, and stays open until
// the user closes it. Ctrl-C still sends figaro.interrupt (just like
// inside a send stream); Ctrl-D disconnects without touching the turn.
func runListen(loaded *config.Loaded, ariaID, recordPath, note string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resolvedID, figaroEP, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		die("%s", err)
	}

	var rec *tape.Writer
	if recordPath != "" {
		// The header is taken BEFORE the dial so its Started is the zero of
		// every frame offset, including the catch-up read the pager fires on
		// its way up.
		rec, err = tape.Create(recordPath, tape.Header{
			Aria:    resolvedID,
			Cols:    term.Width(),
			Rows:    term.Height(),
			Term:    os.Getenv("TERM"),
			Binary:  buildRevision(),
			Command: strings.Join(os.Args, " "),
			Note:    note,
		})
		if err != nil {
			die("record: %s", err)
		}
		defer func() {
			if cerr := rec.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "figaro: tape: %v\n", cerr)
			}
		}()
	}

	tailFigaro(ctx, cancel, figaroEP, resolvedID, loaded, tailOpts{tape: rec})
}

// tailFigaro is the read-only twin of mustPromptFigaro. It opens the
// same incipit-freeze renderer, catches up from LT 0, then follows live
// frames forever. Ctrl-C -> figaro.interrupt; Ctrl-D -> clean
// disconnect (turn keeps running); Ctrl-T -> transcript pager.
// Returns when the user disconnects, the agent socket dies, or ctx
// is canceled.
// tailOpts are the affordances only a non-interactive caller wants. The zero
// value is `figaro listen` exactly, which is why they are a struct and not
// three more positional parameters: the ordinary path names none of them.
type tailOpts struct {
	// tape records the wire (nil = record nothing).
	tape *tape.Writer
	// end closes when the stream is over by the caller's own reckoning: the
	// end of a replayed tape. It joins the SAME select the turn-over path uses,
	// so the exit is the clean one and not an invented interrupt.
	end <-chan struct{}
	// startedAt overrides the session clock shown in the status row. A replay
	// passes the recording's own start so the same tape paints the same pixels
	// at any hour; live callers leave it zero and get time.Now.
	startedAt time.Time
}

func tailFigaro(ctx context.Context, cancel context.CancelFunc, ep transport.Endpoint, figaroID string, loaded *config.Loaded, opt tailOpts) {
	ctx, span := figOtel.Start(ctx, "cli.listen")
	defer span.End()

	startedAt := opt.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	status := newSessionStatus(figaroID, startedAt)

	// We want Ctrl-C to mean "interrupt the in-flight turn" (parity
	// with send). Wrap the parent ctx so SIGINT cancels just our scope.
	ctx, sigCancel := signal.NotifyContext(ctx, os.Interrupt)
	defer sigCancel()

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	height := term.Height()

	// Bookend banner: id + start time. Same gating as send.
	var bookendFn func() []string
	if loaded.StatusLine() {
		bookendFn = func() []string { return bookendLines(status) }
	}

	set := renderSettings{listen: true} // listen stays open past turn-done
	lt := newLivelogTurn(os.Stdout, width, height, &set, figaroID, startedAt, status, bookendFn, dimRule)
	tc := term.NewClient()

	// The renderer owns the cursor + auto-margin off, same as send.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer endSession(os.Stdout)
	// See runStream: the deferred right-edge wrap belongs to the painter, not
	// to every command that prints a line.
	restoreWrap := sync.OnceFunc(term.ArmDeferredWrap())
	defer restoreWrap()
	atExit(restoreWrap)
	defer lt.leaveTranscript()
	lt.openRule() // the renderer owns the rule AND the margin under it

	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1)

	// mu serializes every renderer entry point; handing it to the pager arms
	// the frame-rate ceiling, whose trailing repaint runs on a timer goroutine
	// and so needs the same lock.
	lt.setRenderLock(&mu)

	onNotify := func(method string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case rpc.MethodAriaFrame:
			var r aria.Page
			if json.Unmarshal(params, &r) == nil {
				lt.apply(r)
			}
		case rpc.MethodTurnDone:
			// listen is a tail: we don't exit on turn boundaries.
			// Just surface error reasons so the user sees them.
			var d rpc.DoneEntry
			_ = json.Unmarshal(params, &d)
			lt.finishTurn(d.Reason)
			if strings.HasPrefix(d.Reason, "error:") {
				sessionLine(os.Stderr, "\r\n"+d.Reason)
			}
		}
	}

	fcli, err := figaro.DialClientWith(ep, onNotify, tapeTap(opt.tape))
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	// On desync, re-read from the highest fully sealed turn.
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

	// figaro listen opens directly in the transcript (its home): load the recent
	// window; older history pages in on scroll-up and live frames follow.
	in := &interactiveInput{
		tc: tc, lt: lt, fcli: fcli, hangup: fcli, mu: &mu, set: &set,
		figaroID: figaroID, cancel: cancel, disconnectCh: disconnectCh,
	}
	// listen opens the pager through the deliberate door below, which reads; the
	// hook is armed anyway so a promotion that happens some other way (a resize
	// after an explicit exit, say) still owes and pays for its history.
	in.lt.setCatchUp(in.pagerCatchUp)
	in.enterTranscript()

	// Local spinner animation.
	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / spinnerFPS)
		defer t.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-t.C:
				mu.Lock()
				lt.tick()
				mu.Unlock()
			}
		}
	}()
	defer close(stopTick)

	// Resize (SIGWINCH on unix / a console event on Windows, behind the client).
	defer tc.OnResize(func(w, h int) {
		mu.Lock()
		lt.resize(w, h)
		mu.Unlock()
	})()

	// Keybindings: the same control keys + pager as send, via the shared loop.
	// MakeRaw so Ctrl-C/Ctrl-D arrive as bytes.
	if tc.IsTTY() {
		if restore, err := tc.MakeRaw(); err == nil {
			defer restore()
			fmt.Fprint(os.Stdout, enableModifiedKeyReporting)
			defer fmt.Fprint(os.Stdout, disableModifiedKeyReporting)
			defer os.Stdout.WriteString(ldmouse.Disable)
			go in.run()
		} else {
			fmt.Fprintf(os.Stderr, "figaro: terminal input disabled: enter raw mode: %v\n", err)
		}
	}

	select {
	case <-doneCh:
	case <-opt.end:
		// The tape ran out. Leave the way a finished turn leaves.
		lt.finishTurn("")
	case <-disconnectCh:
		lt.abandon(turnStatusDisconnected)
	case <-fcli.Done():
		lt.abandon(turnStatusError)
	case <-ctx.Done():
		// Ctrl-C from signal.NotifyContext: interrupt the turn, then leave.
		sessionLine(os.Stderr, "\r\ninterrupting...")
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()
		lt.abandon(turnStatusInterrupted)
	}
}
