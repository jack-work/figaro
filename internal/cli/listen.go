package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
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
// same inline-seal renderer, catches up from LT 0, then follows live
// frames forever. Ctrl-C -> figaro.interrupt; Ctrl-D -> clean
// disconnect (turn keeps running); Ctrl-T -> transcript pager.
// Returns when the user disconnects, the agent socket dies, or ctx
// is canceled.
func tailFigaro(ctx context.Context, cancel context.CancelFunc, ep transport.Endpoint, figaroID string, loaded *config.Loaded) {
	ctx, span := figOtel.Start(ctx, "cli.listen")
	defer span.End()

	startedAt := time.Now()

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
		bookendFn = func() string { return statusBanner(figaroID, startedAt) }
	}

	set := renderSettings{}
	lt := newLivelogTurn(os.Stdout, width, height, &set, bookendFn, dimRule)

	// Inline mode owns the cursor + auto-margin off, same as send.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+autowrapOn)
	defer func() {
		if lt.transcriptActive() {
			fmt.Fprint(os.Stdout, altScreenOff)
		}
	}()
	fmt.Fprintln(os.Stdout, dimRule())

	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1)

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

	// Catch up to full history. Live frames will follow on the same connection.
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	r, rerr := fcli.Read(rctx, 0)
	rcancel()
	if rerr == nil {
		mu.Lock()
		lt.apply(r)
		mu.Unlock()
	}

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

	// Resize handling.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			w, h := term.Width(), term.Height()
			mu.Lock()
			lt.resize(w, h)
			mu.Unlock()
		}
	}()

	// Keybindings: same as the send stream, minus the turn-end gate.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if restore, err := term.MakeCbreak(int(os.Stdin.Fd())); err == nil {
			defer restore()
			go func() {
				buf := make([]byte, 64)
				for {
					n, err := os.Stdin.Read(buf)
					if err != nil {
						cancel()
						return
					}
					for _, b := range buf[:n] {
						mu.Lock()
						active := lt.transcriptActive()
						mu.Unlock()
						if active {
							mu.Lock()
							exited := lt.transcriptKey(b)
							mu.Unlock()
							_ = exited // never auto-close listen on pager exit
							continue
						}
						switch b {
						case 0x04: // Ctrl-D: disconnect; don't interrupt
							select {
							case disconnectCh <- struct{}{}:
							default:
							}
							return
						case 0x0f: // Ctrl-O: verbose toggle
							mu.Lock()
							set.verbose = !set.verbose
							lt.render()
							mu.Unlock()
						case 0x14: // Ctrl-T: open transcript pager
							rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
							r, rerr := fcli.Read(rctx, 0)
							rcancel()
							mu.Lock()
							lt.enterTranscript()
							if rerr == nil {
								lt.apply(r)
							}
							mu.Unlock()
						}
					}
				}
			}()
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
