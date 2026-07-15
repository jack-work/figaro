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
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
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

	lt := newLivelogTurn(os.Stdout, width, height, &set, bookendFn, dimRule)

	// The renderer owns the cursor and assumes one row per line: disable the
	// terminal's auto-margin so a full-width row never wraps, and hide the
	// cursor. It draws inline (no alternate screen) — sealed output lands in the
	// normal scrollback.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+autowrapOn)
	defer func() {
		if lt.transcriptActive() {
			fmt.Fprint(os.Stdout, altScreenOff)
		}
	}()

	// Static opening rule: a single dim horizontal line separating the user's
	// shell prompt from the response stream. Printed once, lives in scrollback.
	fmt.Fprintln(os.Stdout, dimRule())

	// The renderer is single-threaded; the notify pump, the spinner ticker, the
	// SIGWINCH handler, and keybindings all serialize on mu.
	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1) // Ctrl-D: leave the turn running
	turnDone := false // turn ended while the pager was up; exit on pager close
	sendCursor := -1  // cursor from Qua; stop only once committed past it and idle

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
			isErr := strings.HasPrefix(d.Reason, "error:")
			if isErr {
				if strings.Contains(d.Reason, "no credential") || strings.Contains(d.Reason, "resolve token") {
					fmt.Fprint(os.Stderr, "\n"+providerSetupHint())
				} else {
					fmt.Fprintln(os.Stderr, "\n"+d.Reason)
				}
			}
			// Settle when the agent reports idle (inbox empty, no turn running):
			// a turn that ended with our steer still queued reports idle=false,
			// so we correctly wait for our own turn. A daemon predating the idle
			// field sends nil — treat that as settled (the pre-steering behavior),
			// so an old running daemon doesn't strand the command. We only act
			// once our prompt has been submitted (sendCursor set after Qua
			// returns), so a turn.done that predates our send can't end us early.
			// An error always settles. (Do NOT gate on lt.cursor() advancing —
			// the final commit can arrive via async desync recovery AFTER this
			// one-shot turn.done, which would strand us and hang the command.)
			idle := d.Idle == nil || *d.Idle
			settled := isErr || (sendCursor >= 0 && idle)
			if !settled {
				break
			}
			if lt.transcriptActive() {
				turnDone = true // let the user keep reading; exit when they close the pager
			} else {
				select {
				case doneCh <- struct{}{}:
				default:
				}
			}
		}
	}

	fcli, err := figaro.DialClient(ep, onNotify)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	// On a version desync, re-read from the last fully-committed LT and re-apply
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
	// toggles verbosity / opens the pager; Ctrl-D disconnects the CLI without
	// touching the turn (no figaro.interrupt) — the daemon keeps running.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if restore, err := term.MakeCbreak(int(os.Stdin.Fd())); err == nil {
			defer restore()
			// Belt-and-braces: always disable mouse reporting on exit (harmless
			// if never enabled) so a crash/interrupt mid-pager can't leave the
			// shell spewing raw \x1b[<…M. Normal pager exit disables it earlier.
			defer os.Stdout.WriteString(ldmouse.Disable)
			go func() {
				buf := make([]byte, 64)
				var pending []byte // a mouse/escape sequence split across reads
				for {
					n, err := os.Stdin.Read(buf)
					if err != nil {
						cancel()
						return
					}
					data := append(pending, buf[:n]...)
					pending = nil
					i := 0
					for i < len(data) {
						mu.Lock()
						active := lt.transcriptActive()
						mu.Unlock()
						if active {
							// Native mouse wheel scrolls the pager; other keys drive it.
							if ev, consumed, ok, need := ldmouse.Parse(data[i:]); need {
								pending = append(pending, data[i:]...) // wait for the rest
								break
							} else if ok {
								i += consumed
								delta := 0
								switch ev.Button {
								case ldmouse.WheelUp:
									delta = -3
								case ldmouse.WheelDown:
									delta = 3
								}
								if delta != 0 {
									mu.Lock()
									lt.transcriptScroll(delta)
									mu.Unlock()
								}
								continue
							}
							b := data[i]
							i++
							mu.Lock()
							exited := lt.transcriptKey(b)
							fire := exited && turnDone
							mu.Unlock()
							if fire {
								select {
								case doneCh <- struct{}{}:
								default:
								}
							}
							continue
						}
						b := data[i]
						i++
						switch b {
						case 0x04: // Ctrl-D: disconnect the CLI; do NOT interrupt the turn
							select {
							case disconnectCh <- struct{}{}:
							default:
							}
							return
						case 0x0f: // Ctrl-O: toggle verbosity
							mu.Lock()
							set.verbose = !set.verbose
							lt.render()
							mu.Unlock()
						case 0x14: // Ctrl-T: open the full-screen transcript pager
							rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
							r, rerr := fcli.Read(rctx, 0) // catch the model up to full history
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

	cursor, qerr := fcli.Qua(ctx, prompt, buildPromptChalkboard())
	if qerr != nil {
		die("prompt: %s", qerr)
	}
	mu.Lock()
	sendCursor = cursor
	mu.Unlock()

	select {
	case <-doneCh:
		// The committed bookend is the final line; nothing more to print.
	case <-disconnectCh:
		lt.abandon("disconnected — turn continues")
		fmt.Fprintln(os.Stderr, "follow: figaro listen "+figaroID)
	case <-fcli.Done():
		lt.abandon("agent disconnected before turn completed")
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
			lt.abandon("interrupted (agent did not respond)")
		}
		fmt.Fprintln(os.Stderr, "interrupted")
	}
}

// dimRule returns a plain dim full-width horizontal rule — the opening rule and
// the seal after a non-assistant (user/steering) message.
func dimRule() string { return term.Dim(strings.Repeat("─", termWidth())) }

// abandonRule returns a labeled dim rule used when a live region ends without
// a normal seal (crash, disconnect, interrupt-timeout). Shape: "─── [reason] ───..."
func abandonRule(reason string) string {
	w := termWidth()
	body := " [" + reason + "] "
	prefix := "───" + body
	fill := w - len(prefix)
	if fill < 3 {
		fill = 3
	}
	return term.Dim(prefix + strings.Repeat("─", fill))
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
