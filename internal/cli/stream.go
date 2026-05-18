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

	pace := pacer.New(sw, pacer.Options{
		TargetCPS:       loaded.StreamCPS(),
		FirstByteBypass: time.Duration(loaded.StreamFirstByteBypassMs()) * time.Millisecond,
	})
	defer pace.Close()

	var rawOut io.Writer
	openTools := 0

	var batch *toolBatchState
	var solo *toolSoloState
	var soloToolCallID string // tool_call_id the active solo was launched for

	// Accumulates partial tool input JSON per tool_call_id so the
	// CLI can extract and render tool details (command, path)
	// progressively as deltas arrive rather than waiting for the
	// full message. Maps tool_call_id → {toolName, partialJSON}.
	pendingToolArgs := map[string]*pendingToolArg{}

	resumeIfSuspended := func() {
		// Wrapped-batch teardown takes precedence over solo.Freeze.
		if batch != nil {
			if batch.wrapped && solo != nil {
				anyErr := batch.FinalizeRowsOnly()
				solo.Done(anyErr)
				solo = nil
				soloToolCallID = ""
				batch.PrintErrorDumps()
			} else {
				batch.Finalize()
			}
			batch = nil
		}
		if solo != nil {
			solo.Freeze()
			solo = nil
			soloToolCallID = ""
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

		case rpc.MethodToolUseStart:
			// Show a placeholder spinner for the first tool_use.
			var p rpc.ToolUseStartParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.tool_use_start",
					attribute.String("tool", p.ToolName),
					attribute.String("tool_call_id", p.ToolCallID),
				)
				pendingToolArgs[p.ToolCallID] = &pendingToolArg{toolName: p.ToolName}
				if batch == nil && solo == nil {
					pace.Flush()
					sw.Flush()
					if rawOut == nil {
						rawOut = sw.Suspend()
					}
					solo = newToolSoloState(rawOut, p.ToolName, "")
					solo.Start()
					soloToolCallID = p.ToolCallID
				}
			}

		case rpc.MethodToolUseDelta:
			var p rpc.ToolUseDeltaParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.tool_use_delta",
					attribute.String("tool_call_id", p.ToolCallID),
					attribute.Int("bytes", len(p.PartialJSON)),
				)
				if pt, ok := pendingToolArgs[p.ToolCallID]; ok {
					pt.json += p.PartialJSON
					if detail := extractPartialDetail(pt.toolName, pt.json); detail != "" {
						if solo != nil && soloToolCallID == p.ToolCallID {
							solo.UpdateDetail(detail)
						}
					}
				}
			}

		case rpc.MethodToolBatchStart:
			var p rpc.ToolBatchStartParams
			if json.Unmarshal(params, &p) == nil && p.Size > 1 {
				figOtel.Event(ctx, "cli.recv.tool_batch_start",
					attribute.Int("size", p.Size))
				// Repurpose placeholder spinner as batch wrapper.
				wrapped := false
				if solo != nil {
					solo.UpdateHeader("batch", fmt.Sprintf("(%d)", p.Size))
					// Hand spinner duty to batch's ticker.
					solo.StopTicker()
					wrapped = true
				} else {
					pace.Flush()
					sw.Flush()
					if rawOut == nil {
						rawOut = sw.Suspend()
					}
				}
				batch = newToolBatchState(rawOut, p.Tools)
				batch.wrapped = wrapped
				if wrapped {
					batch.wrapperSolo = solo
				}
				batch.RenderInitial()
			}

		case rpc.MethodToolBatchEnd:
			figOtel.Event(ctx, "cli.recv.tool_batch_end")
			if batch != nil {
				if batch.wrapped && solo != nil {
					anyErr := batch.FinalizeRowsOnly()
					solo.Done(anyErr)
					solo = nil
					soloToolCallID = ""
					fmt.Fprintln(rawOut, term.Dim("───"))
					batch.PrintErrorDumps()
				} else {
					batch.Finalize()
				}
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
				// Reuse existing placeholder or create one.
				if solo != nil && soloToolCallID == p.ToolCallID {
					solo.UpdateDetail(toolDetail(p))
				} else {
					if rawOut == nil {
						pace.Flush()
						sw.Flush()
						rawOut = sw.Suspend()
					}
					solo = newToolSoloState(rawOut, p.ToolName, toolDetail(p))
					solo.Start()
					soloToolCallID = p.ToolCallID
				}
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
