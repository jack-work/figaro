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

const spinnerFPS = 11 // spinner frames per second (~90ms/frame)

// Terminal control: disable auto-margin (so a full-width row never wraps) and
// hide the cursor while the renderer owns the screen.
const (
	autowrapOff = "\x1b[?7l"
	autowrapOn  = "\x1b[?7h"
	cursorHide  = "\x1b[?25l"
	cursorShow  = "\x1b[?25h"
)

// mustPromptFigaro is the interactive (TTY) prompt path. It renders the
// aria-read wire through the inline-seal renderer: closed messages seal to
// native scrollback once and are never redrawn; only the open message is a live
// region, so a terminal resize repaints just that bounded part. The renderer
// folds each aria frame and animates spinners locally (no extra wire traffic).
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded, set renderSettings) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	startedAt := time.Now()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	height := term.Height()

	// Bookend: a status rule (aria id + start time) pinned just below the
	// agent's reply. Gated on the status-line config.
	var bookendFn func() string
	if loaded.StatusLine() {
		bookendFn = func() string { return statusBanner(figaroID, startedAt) }
	}

	lt := newLivelogTurn(os.Stdout, width, height, &set, bookendFn)

	// The renderer owns the cursor and assumes one row per line: disable the
	// terminal's auto-margin so a full-width row never wraps, and hide the
	// cursor. It draws inline (no alternate screen) — sealed output lands in the
	// normal scrollback.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+autowrapOn)

	// The renderer is single-threaded; the notify pump, the spinner ticker, the
	// SIGWINCH handler, and keybindings all serialize on mu.
	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)

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
			var d rpc.DoneEntry
			_ = json.Unmarshal(params, &d)
			if strings.HasPrefix(d.Reason, "error:") {
				fmt.Fprintln(os.Stderr, "\n"+d.Reason)
			}
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	}

	fcli, err := figaro.DialClient(ep, onNotify)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	// Local spinner animation: ticks the open message's running tool; zero extra
	// wire traffic (output streams via aria frames).
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

	// Repaint the open message on resize.
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

	// Live keybindings (cbreak: per-key, no echo, SIGINT intact). Ctrl-O/Ctrl-T
	// toggles verbosity and repaints the open message; Ctrl-D ends the turn.
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
						switch b {
						case 0x04: // Ctrl-D
							cancel()
							return
						case 0x0f, 0x14: // Ctrl-O / Ctrl-T: toggle verbosity
							mu.Lock()
							set.verbose = !set.verbose
							lt.render()
							mu.Unlock()
						}
					}
				}
			}()
		}
	}

	if err := fcli.Qua(ctx, prompt, buildPromptChalkboard()); err != nil {
		die("prompt: %s", err)
	}

	select {
	case <-doneCh:
		// The committed bookend is the final line; nothing more to print.
	case <-fcli.Done():
		fmt.Fprintln(os.Stderr, "\nerror: agent disconnected before turn completed")
		os.Exit(1)
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "\ninterrupting...")
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()
		select {
		case <-doneCh:
		case <-fcli.Done():
		case <-time.After(3 * time.Second):
		}
		fmt.Fprintln(os.Stderr, "interrupted")
	}
}

// statusBanner returns a full-width dimmed bookend: "─── id · time ───…"
// extended with box-drawing dashes to the viewport width.
func statusBanner(figaroID string, ts time.Time) string {
	body := fmt.Sprintf("%s · %s", figaroID, ts.Format("15:04:05"))
	prefix := "─── " + body + " " // 4 cols + body + 1 col (body is ASCII)
	fill := termWidth() - 4 - len(body) - 1
	if fill < 3 {
		fill = 3
	}
	return term.Dim(prefix + strings.Repeat("─", fill))
}

// termWidth returns the terminal width, defaulting to 80.
func termWidth() int {
	if w := term.Width(); w > 0 {
		return w
	}
	return 80
}
