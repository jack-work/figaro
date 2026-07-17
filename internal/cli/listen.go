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
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// runListen tails an aria with the same renderer the rich send uses,
// minus the Qua call: it catches up to the committed cursor, follows
// live frames, supports Ctrl-T transcript mode, and stays open until
// the user closes it. Ctrl-C still sends figaro.interrupt (just like
// inside a send stream); Ctrl-D disconnects without touching the turn.
//
// With no ariaID, the pid-bound aria is used.
func runListen(loaded *config.Loaded, ariaID string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resolvedID, figaroEP, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false)
	if err != nil {
		die("%s", err)
	}

	tailFigaro(ctx, cancel, figaroEP, resolvedID, loaded)
}

// tailFigaro is the read-only twin of mustPromptFigaro. It opens the
// same incipit-seal renderer, catches up from LT 0, then follows live
// frames forever. Ctrl-C -> figaro.interrupt; Ctrl-D -> clean
// disconnect (turn keeps running); Ctrl-T -> transcript pager.
// Returns when the user disconnects, the agent socket dies, or ctx
// is canceled.
func tailFigaro(ctx context.Context, cancel context.CancelFunc, ep transport.Endpoint, figaroID string, loaded *config.Loaded) {
	ctx, span := figOtel.Start(ctx, "cli.listen")
	defer span.End()

	startedAt := time.Now()
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
	var bookendFn func() string
	if loaded.StatusLine() {
		bookendFn = func() string { return statusBanner(status) }
	}

	set := renderSettings{listen: true} // listen stays open past turn-done
	lt := newLivelogTurn(os.Stdout, width, height, &set, figaroID, startedAt, status, bookendFn, dimRule)
	tc := term.NewClient()

	// The renderer owns the cursor + auto-margin off, same as send.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+autowrapOn)
	defer lt.leaveTranscript()
	fmt.Fprintln(os.Stdout, dimRule())

	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1)
	listen := true

	onNotify := func(method string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case rpc.MethodAriaFrame:
			var r aria.AriaRead
			if json.Unmarshal(params, &r) == nil {
				lt.apply(r)
			}
		case rpc.MethodTurnDone:
			// listen is a tail — we don't exit on turn boundaries.
			// Just surface error reasons so the user sees them.
			var d rpc.DoneEntry
			_ = json.Unmarshal(params, &d)
			if strings.HasPrefix(d.Reason, "error:") {
				fmt.Fprintln(os.Stderr, "\n"+d.Reason)
			}
		}
	}

	fcli, err := figaro.DialClient(ep, onNotify)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	// On desync, re-read from the last fully-committed LT.
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
		tc: tc, lt: lt, fcli: fcli, mu: &mu, set: &set,
		figaroID: figaroID, listen: &listen, cancel: cancel, disconnectCh: disconnectCh,
	}
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

	// Keybindings — the same control keys + pager as send, via the shared loop.
	// MakeRaw so Ctrl-C/Ctrl-D arrive as bytes.
	if tc.IsTTY() {
		if restore, err := tc.MakeRaw(); err == nil {
			defer restore()
			defer os.Stdout.WriteString(ldmouse.Disable)
			go in.run()
		}
	}

	select {
	case <-doneCh:
	case <-disconnectCh:
		lt.abandon("disconnected — turn (if any) continues")
	case <-fcli.Done():
		lt.abandon("aria disconnected")
	case <-ctx.Done():
		// Ctrl-C from signal.NotifyContext: interrupt the turn, then leave.
		fmt.Fprintln(os.Stderr, "\ninterrupting...")
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()
		lt.abandon("interrupted")
	}
}
