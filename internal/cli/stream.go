package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jack-work/largo"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/pacer"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// mustPromptFigaro is the interactive prompt path.
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()


	startedAt := time.Now()
	if loaded.StatusLine() {
		writeStatusLine(os.Stdout, figaroID, startedAt, 0)
	}
	if loaded.EchoPrompt() {
		fmt.Println()
		fmt.Println("> " + prompt)
		fmt.Println()
		writeSeparator(os.Stdout)
	} else if loaded.StatusLine() {
		fmt.Println()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Watch stdin for EOF (Ctrl+D).
	if term.IsTerminal(int(os.Stdin.Fd())) {
		go func() {
			buf := make([]byte, 256)
			for {
				_, err := os.Stdin.Read(buf)
				if err != nil {
					cancel()
					return
				}
			}
		}()
	}

	doneCh := make(chan struct{}, 1)

	sw, err := largo.NewWriter(os.Stdout, largo.Options{})
	if err != nil {
		die("largo: %s", err)
	}

	renderer := newStreamRenderer(ctx, sw)
	pace := pacer.New(renderer.PacedOut(), pacer.Options{
		TargetCPS:       loaded.StreamCPS(),
		FirstByteBypass: time.Duration(loaded.StreamFirstByteBypassMs()) * time.Millisecond,
	})
	defer pace.Close()
	renderer.SetPacer(pace)
	go func() {
		<-renderer.Done()
		select {
		case doneCh <- struct{}{}:
		default:
		}
	}()

	deliverEvent := func(method string, params json.RawMessage) {
		renderer.Handle(method, params)
	}

	fcli, err := figaro.DialClient(ep, func(method string, params json.RawMessage) {
		deliverEvent(method, params)
	})
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	if err := fcli.Qua(ctx, prompt, buildPromptChalkboard()); err != nil {
		die("prompt: %s", err)
	}

	select {
	case <-doneCh:
		fmt.Println()
	case <-fcli.Done():
		pace.Flush()
		renderer.resumeIfSuspended()
		sw.Flush()
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
		pace.Flush()
		renderer.resumeIfSuspended()
		sw.Flush()
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

// toolDetail returns a one-line summary for a tool call.
func toolDetail(p rpc.ToolStartParams) string {
	switch p.ToolName {
	case "bash":
		if cmd, ok := p.Arguments["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit":
		if path, ok := p.Arguments["path"].(string); ok {
			return path
		}
	}
	return ""
}

// pendingToolArg accumulates streamed tool input JSON so the CLI can
// extract the detail string (command, path) progressively.
type pendingToolArg struct {
	toolName string
	json     string
}

// extractPartialDetail pulls the displayable detail from an
// incomplete JSON input string. The Anthropic API streams tool
// arguments left-to-right as JSON text, so we see:
//
//	{"command": "figaro --help 2>&1 | hea
//
// before the string (or the object) is closed. We find the value
// for the key we care about and return whatever we have so far.
func extractPartialDetail(toolName, partial string) string {
	var key string
	switch toolName {
	case "bash":
		key = `"command": "`
	case "read", "write", "edit":
		key = `"path": "`
	default:
		return ""
	}

	idx := strings.Index(partial, key)
	if idx < 0 {
		return ""
	}
	// Start after the opening quote of the value.
	valStart := idx + len(key)
	if valStart >= len(partial) {
		return ""
	}

	// Scan forward for the closing quote, handling \" escapes.
	// If we don't find one, return everything we have (it's still
	// streaming).
	var b strings.Builder
	for i := valStart; i < len(partial); i++ {
		ch := partial[i]
		if ch == '\\' && i+1 < len(partial) {
			// Escaped character — take the next byte literally.
			i++
			b.WriteByte(partial[i])
			continue
		}
		if ch == '"' {
			// Closing quote — value is complete.
			break
		}
		b.WriteByte(ch)
	}
	return b.String()
}
