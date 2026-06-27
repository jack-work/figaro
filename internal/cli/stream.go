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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	lr := newLiveRegion(os.Stdout, width, 0) // 0 → renderer's default bash cap (10)
	lr.height = term.Height()
	lr.settings = set

	// The painter owns the cursor and assumes one row per line: disable the
	// terminal's auto-margin so a full-width row never wraps, and hide the
	// text cursor so it doesn't sit on the live line. Both restored on exit.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+autowrapOn)

	// Bookend: a status rule (aria id + start time) pinned just below the
	// agent's reply and persisting as the final line after the turn. Gated
	// on the status-line config.
	var bookendFn func() string
	if loaded.StatusLine() {
		bookendFn = func() string { return statusBanner(figaroID, startedAt) }
	}

	// liveRegion is single-threaded; the notify pump, the spinner ticker,
	// and SIGWINCH all serialize on lrMu.
	var lrMu sync.Mutex
	doneCh := make(chan struct{}, 1)
	prevRole := "" // role of the last unit snapshot, for inter-unit separation

	// Render trace: FIGARO_WIRE_LOG=<file> records the EXACT ordered stream of
	// painter actions — every received frame AND every spinner tick,
	// timestamped — while rendering normally. A glitch caught in real use then
	// replays deterministically offline (no agent, no tokens): the file order is
	// the order the painter applied actions (everything serializes on lrMu).
	// Line shapes: {"op":"init","w":N,"h":N} | {"t":ns,"op":"frame","method","params"} | {"t":ns,"op":"tick"}.
	trace := func(string, string, json.RawMessage) {}
	if p := os.Getenv("FIGARO_WIRE_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			defer f.Close()
			start := time.Now()
			fmt.Fprintf(f, "{\"op\":\"init\",\"w\":%d,\"h\":%d}\n", width, lr.height)
			trace = func(op, method string, params json.RawMessage) {
				rec := struct {
					T      int64           `json:"t"`
					Op     string          `json:"op"`
					Method string          `json:"method,omitempty"`
					Params json.RawMessage `json:"params,omitempty"`
				}{time.Since(start).Nanoseconds(), op, method, params}
				if line, err := json.Marshal(rec); err == nil {
					f.Write(append(line, '\n'))
				}
			}
		} else {
			fmt.Fprintln(os.Stderr, "warning: FIGARO_WIRE_LOG open failed:", err)
		}
	}

	onNotify := func(method string, params json.RawMessage) {
		lrMu.Lock()
		defer lrMu.Unlock()
		trace("frame", method, params)
		switch method {
		case rpc.MethodLogSnapshot:
			var e rpc.SnapshotEntry
			if json.Unmarshal(params, &e) == nil {
				// Separate figaro's echoed prompt from the verbatim
				// command line the user typed into the shell, and set off
				// the agent's reply from that echoed prompt. Each is a rule
				// + blank line above plain scrollback (the cursor is parked
				// below the prior unit, above the new live region).
				if e.Role == "user" && prevRole == "" {
					fmt.Fprint(os.Stdout, dimRule(width)+"\n\n")
				}
				if e.Role == "assistant" && prevRole == "user" {
					fmt.Fprint(os.Stdout, dimRule(width)+"\n\n")
				}
				// The bookend follows the live agent unit only; the echoed
				// prompt unit has none.
				if e.Role == "assistant" {
					lr.bookend = bookendFn
				} else {
					lr.bookend = nil
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
				lr.applyOp(livedoc.Op{Kind: livedoc.OpSet, Index: e.Index, Status: e.Status, Name: e.Name, Args: e.Args})
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
				trace("tick", "", nil)
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
			lr.height = term.Height()
			lr.resize(w)
			lrMu.Unlock()
		}
	}()

	// Live keybindings: in cbreak mode (per-key, no echo, SIGINT intact)
	// Ctrl-O (or Ctrl-T, its alias) toggles verbosity — expanded tool inputs —
	// re-rendering the open unit immediately. Ctrl-D ends the turn. Non-TTY
	// input is skipped (the flag still sets the initial state).
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
						case 0x0f, 0x14: // Ctrl-O / Ctrl-T (alias): toggle verbosity
							lrMu.Lock()
							set.verbose = !set.verbose
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
		// The committed bookend is the final line; commit left the cursor on
		// a fresh line below it. Nothing more to print.
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
