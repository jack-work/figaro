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
	"github.com/jack-work/figaro/internal/livedoc"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

const spinnerFPS = 11 // spinner frames per second (~90ms/frame)

// mustPromptFigaro is the interactive (TTY) prompt path: it drives a
// liveRegion from the snapshot/delta/commit frame stream, animating
// spinners locally and reflowing on resize.
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded, set renderSettings) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	startedAt := time.Now()
	if loaded.StatusLine() {
		writeStatusLine(os.Stdout, figaroID, startedAt, 0)
		fmt.Println()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	lr := newLiveRegion(os.Stdout, width, 0) // 0 → renderer's default bash cap (10)
	lr.settings = set

	// The painter owns the cursor and assumes one row per line; disable the
	// terminal's auto-margin for the live session so a full-width row never
	// wraps and desyncs that math. Restored on exit (incl. interrupt).
	fmt.Fprint(os.Stdout, autowrapOff)
	defer fmt.Fprint(os.Stdout, autowrapOn)

	// liveRegion is single-threaded; the notify pump, the spinner ticker,
	// and SIGWINCH all serialize on lrMu.
	var lrMu sync.Mutex
	doneCh := make(chan struct{}, 1)
	prevRole := "" // role of the last unit snapshot, for inter-unit separation

	onNotify := func(method string, params json.RawMessage) {
		lrMu.Lock()
		defer lrMu.Unlock()
		switch method {
		case rpc.MethodLogSnapshot:
			var e rpc.SnapshotEntry
			if json.Unmarshal(params, &e) == nil {
				// Set off the agent's reply from the echoed prompt with a
				// rule and a blank line (the cursor is parked below the
				// just-committed user unit; this is plain scrollback above
				// the new live region).
				if e.Role == "assistant" && prevRole == "user" {
					fmt.Fprint(os.Stdout, dimRule(width)+"\n\n")
				}
				prevRole = e.Role
				lr.snapshot(e.Nodes)
			}
		case rpc.MethodNodeOpen:
			var e rpc.NodeOpenEntry
			if json.Unmarshal(params, &e) == nil {
				lr.applyOp(livedoc.Op{Kind: livedoc.OpOpen, Index: e.Index, Node: &e.Node})
			}
		case rpc.MethodNodePatch:
			var e rpc.NodePatchEntry
			if json.Unmarshal(params, &e) == nil {
				lr.applyOp(livedoc.Op{Kind: livedoc.OpPatch, Index: e.Index, Field: e.Field, At: e.At, Del: e.Del, Ins: e.Ins})
			}
		case rpc.MethodNodeSet:
			var e rpc.NodeSetEntry
			if json.Unmarshal(params, &e) == nil {
				lr.applyOp(livedoc.Op{Kind: livedoc.OpSet, Index: e.Index, Status: e.Status})
			}
		case rpc.MethodLogCommit:
			lr.commit()
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

	// Local spinner animation: ticks any running tool's spinner; zero
	// extra wire traffic (output streams via server node ops).
	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / spinnerFPS)
		defer t.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-t.C:
				lrMu.Lock()
				lr.tickSpin()
				lrMu.Unlock()
			}
		}
	}()
	defer close(stopTick)

	// Reflow the live tail on resize.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			w := term.Width()
			lrMu.Lock()
			lr.resize(w)
			lrMu.Unlock()
		}
	}()

	// Live keybindings: in cbreak mode (per-key, no echo, SIGINT intact)
	// Ctrl-O toggles tool-input expansion and Ctrl-T toggles thinking,
	// re-rendering the open unit immediately. Ctrl-D ends the turn. Non-TTY
	// input is skipped (the flags still set the initial state).
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
						case 0x0f: // Ctrl-O
							lrMu.Lock()
							set.expandTools = !set.expandTools
							lr.setSettings(set)
							lrMu.Unlock()
						case 0x14: // Ctrl-T
							lrMu.Lock()
							set.showThinking = !set.showThinking
							lr.setSettings(set)
							lrMu.Unlock()
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
		fmt.Println()
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

	if loaded.StatusLine() {
		writeStatusLine(os.Stdout, figaroID, time.Now(), time.Since(startedAt))
	}
}

// writeStatusLine prints a full-width dimmed banner: "─── id · time ───…"
// extended with box-drawing dashes to the viewport width, so it bookends
// the output as a clean rule.
func writeStatusLine(w *os.File, figaroID string, ts time.Time, elapsed time.Duration) {
	body := fmt.Sprintf("%s · %s", figaroID, ts.Format("15:04:05"))
	if elapsed > 0 {
		body += fmt.Sprintf(" · %s", formatElapsed(elapsed))
	}
	prefix := "─── " + body + " " // 4 cols + body + 1 col (body is ASCII)
	fill := termWidth() - 4 - len(body) - 1
	if fill < 3 {
		fill = 3
	}
	fmt.Fprintln(w, term.Dim(prefix+strings.Repeat("─", fill)))
}

// dimRule returns a dim full-width horizontal rule.
func dimRule(width int) string {
	if width < 3 {
		width = 3
	}
	return term.Dim(strings.Repeat("─", width))
}

// termWidth returns the terminal width, defaulting to 80.
func termWidth() int {
	if w := term.Width(); w > 0 {
		return w
	}
	return 80
}

// writeSeparator prints a dimmed rule.
func writeSeparator(w *os.File) {
	fmt.Fprintln(w, term.Dim("───"))
	fmt.Fprintln(w)
}

func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Truncate(100 * time.Millisecond).String()
	}
}
