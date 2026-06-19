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

const pacingFPS = 30

// mustPromptFigaro is the interactive (TTY) prompt path: it drives a
// liveRegion from the snapshot/delta/commit frame stream, animating
// spinners locally and reflowing on resize.
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	startedAt := time.Now()
	if loaded.StatusLine() {
		writeStatusLine(os.Stdout, figaroID, startedAt, 0)
		fmt.Println()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Watch stdin for EOF (Ctrl+D).
	if term.IsTerminal(int(os.Stdin.Fd())) {
		go func() {
			buf := make([]byte, 256)
			for {
				if _, err := os.Stdin.Read(buf); err != nil {
					cancel()
					return
				}
			}
		}()
	}

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	lr := newLiveRegion(os.Stdout, width, 0) // 0 → render's default bash cap (10)
	pl := newPacedLive(lr, loaded.StreamCPS(), pacingFPS)

	// pacedLive is single-threaded; the notify pump, the pacing/spinner
	// ticker, and SIGWINCH all serialize on lrMu.
	var lrMu sync.Mutex
	doneCh := make(chan struct{}, 1)

	onNotify := func(method string, params json.RawMessage) {
		lrMu.Lock()
		defer lrMu.Unlock()
		switch method {
		case rpc.MethodLogSnapshot:
			var e rpc.SnapshotEntry
			if json.Unmarshal(params, &e) == nil {
				pl.snapshot(e.Markdown)
			}
		case rpc.MethodLogDelta:
			var e rpc.DeltaEntry
			if json.Unmarshal(params, &e) == nil {
				pl.queueDelta(livedoc.Delta{At: e.At, Del: e.Del, Ins: e.Ins})
			}
		case rpc.MethodLogCommit:
			pl.commit()
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

	// Pacing + local spinner animation: reveals queued deltas typewriter-
	// style and ticks the spinner; zero extra wire traffic.
	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / pacingFPS)
		defer t.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-t.C:
				lrMu.Lock()
				pl.tick()
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
			pl.resize(w)
			lrMu.Unlock()
		}
	}()

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

// writeStatusLine prints a short dimmed banner.
func writeStatusLine(w *os.File, figaroID string, ts time.Time, elapsed time.Duration) {
	body := fmt.Sprintf("%s · %s", figaroID, ts.Format("15:04:05"))
	if elapsed > 0 {
		body += fmt.Sprintf(" · %s", formatElapsed(elapsed))
	}
	fmt.Fprintln(w, term.Dim("─── "+body+" ───"))
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
