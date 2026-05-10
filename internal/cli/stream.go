package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jack-work/largo"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/pacer"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// mustPromptFigaro is the canonical interactive prompt path: dial the
// figaro endpoint, ship the prompt, render the streaming response with
// largo + pacer + tool-call decorations, and unwind on done / error /
// interrupt. Used by `figaro -- <prompt>`, `figaro qua`, and `figaro new`.
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	// Frame the turn: top status banner → optional echoed prompt →
	// thin separator → response.
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

	// Watch stdin for EOF (Ctrl+D) in the background. When we detect it,
	// cancel the context — same path as Ctrl+C.
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

	pace := pacer.New(sw, pacer.Options{
		TargetCPS:       loaded.StreamCPS(),
		FirstByteBypass: time.Duration(loaded.StreamFirstByteBypassMs()) * time.Millisecond,
	})
	defer pace.Close()

	var rawOut io.Writer
	openTools := 0

	var batch *toolBatchState
	var solo *toolSoloState

	resumeIfSuspended := func() {
		if solo != nil {
			solo.Freeze()
			solo = nil
		}
		if rawOut == nil {
			return
		}
		_ = sw.Resume()
		rawOut = nil
	}

	deliverEvent := func(method string, params json.RawMessage) {
		slog.Debug("rpc recv", "method", method, "params", json.RawMessage(params))

		switch method {
		case rpc.MethodDelta:
			var p rpc.DeltaParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.delta",
					attribute.String("text", p.Text),
				)
				pace.Push(p.Text)
			}

		case rpc.MethodThinking:
			var p rpc.ThinkingParams
			if json.Unmarshal(params, &p) == nil {
				sw.Write([]byte("\n> *🤔 " + largo.EscapeInline(p.Text) + "*\n\n"))
			}

		case rpc.MethodMessage:
			figOtel.Event(ctx, "cli.recv.message")
			pace.Flush()
			sw.Flush()

		case rpc.MethodToolBatchStart:
			var p rpc.ToolBatchStartParams
			if json.Unmarshal(params, &p) == nil && p.Size > 1 {
				figOtel.Event(ctx, "cli.recv.tool_batch_start",
					attribute.Int("size", p.Size))
				pace.Flush()
				sw.Flush()
				if rawOut == nil {
					rawOut = sw.Suspend()
				}
				batch = newToolBatchState(rawOut, p.Tools)
				batch.RenderInitial()
			}

		case rpc.MethodToolBatchEnd:
			figOtel.Event(ctx, "cli.recv.tool_batch_end")
			if batch != nil {
				batch.Finalize()
				batch = nil
			}
			if openTools == 0 {
				resumeIfSuspended()
			}

		case rpc.MethodToolStart:
			var p rpc.ToolStartParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.tool_start",
					attribute.String("tool", p.ToolName),
					attribute.String("tool_call_id", p.ToolCallID),
				)
				openTools++
				if batch != nil {
					batch.MarkRunning(p.ToolCallID)
					return
				}
				if rawOut == nil {
					pace.Flush()
					sw.Flush()
					rawOut = sw.Suspend()
				}
				solo = newToolSoloState(rawOut, p.ToolName, toolDetail(p))
				solo.Start()
				figOtel.Event(ctx, "cli.tool.first_paint",
					attribute.String("tool", p.ToolName),
					attribute.String("tool_call_id", p.ToolCallID),
				)
			}

		case rpc.MethodToolOutput:
			var p rpc.ToolOutputParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.tool_output",
					attribute.Int("bytes", len(p.Chunk)),
					attribute.String("tool_call_id", p.ToolCallID),
				)
				if batch != nil {
					batch.AppendOutput(p.ToolCallID, p.Chunk)
					return
				}
				if solo != nil {
					solo.Freeze()
					solo.Write([]byte(p.Chunk))
				} else if rawOut != nil {
					rawOut.Write([]byte(p.Chunk))
				}
			}

		case rpc.MethodToolEnd:
			var p rpc.ToolEndParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.tool_end",
					attribute.String("tool", p.ToolName),
					attribute.String("tool_call_id", p.ToolCallID),
					attribute.Bool("error", p.IsError))
				if batch != nil {
					batch.MarkDone(p.ToolCallID, p.Result, p.IsError)
				} else if rawOut != nil {
					if solo != nil {
						solo.Done(p.IsError)
						solo = nil
					}
					if p.IsError {
						rawOut.Write([]byte("\n" + term.Red("⚠ error:") + " " + p.Result + "\n"))
					}
					rawOut.Write([]byte(term.Dim("───") + "\n\n"))
				}
				if openTools > 0 {
					openTools--
				}
				if openTools == 0 && batch == nil {
					resumeIfSuspended()
				}
			}

		case rpc.MethodError:
			var p rpc.ErrorParams
			if json.Unmarshal(params, &p) == nil {
				pace.Flush()
				resumeIfSuspended()
				sw.Write([]byte("\n**❌ error:** " + largo.EscapeInline(p.Message) + "\n\n"))
			}

		case rpc.MethodDone:
			openTools = 0
			if batch != nil {
				batch.Finalize()
				batch = nil
			}
			pace.Flush()
			resumeIfSuspended()
			sw.Flush()
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
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
		resumeIfSuspended()
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
		resumeIfSuspended()
		sw.Flush()
		fmt.Fprintln(os.Stderr, "interrupted")
	}

	if loaded.StatusLine() {
		writeStatusLine(os.Stdout, figaroID, time.Now(), time.Since(startedAt))
	}
}

// writeStatusLine prints a dimmed banner across the terminal width.
// elapsed=0 omits the duration (top banner). When the file isn't a
// TTY we skip ANSI dim and use a fixed 80-col width.
func writeStatusLine(w *os.File, figaroID string, ts time.Time, elapsed time.Duration) {
	width := term.WidthFd(int(w.Fd()))

	body := fmt.Sprintf(" %s · %s", figaroID, ts.Format("15:04:05"))
	if elapsed > 0 {
		body += fmt.Sprintf(" · %s", formatElapsed(elapsed))
	}
	body += " "

	const lead = "─── "
	const glyph = "─"
	// Use rune count for the glyph (3 bytes each in UTF-8).
	leadRunes := len([]rune(lead))
	bodyRunes := len([]rune(body))
	remaining := width - leadRunes - bodyRunes
	if remaining < 0 {
		remaining = 0
	}
	line := lead + body + strings.Repeat(glyph, remaining)

	fmt.Fprintln(w, term.Dim(line))
}

// writeSeparator prints a thin dimmed rule across the terminal
// width — used to visually fence off the echoed prompt from the
// streaming response below it.
func writeSeparator(w *os.File) {
	width := term.WidthFd(int(w.Fd()))
	line := strings.Repeat("─", width)
	fmt.Fprintln(w, term.Dim(line))
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

// toolDetail returns the most useful single-line detail for a tool
// call: the bash command, the file path, etc.
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
