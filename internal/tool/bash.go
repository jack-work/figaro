package tool

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/sandbox"
)

const (
	DefaultYield       = 10 * time.Second
	DefaultHardTimeout = 30 * time.Minute
)

type BashRequest struct {
	Command    string
	Timeout    time.Duration
	PTY        bool
	YieldMs    time.Duration
	Background bool
}

type BashResult struct {
	Output       string
	ExitCode     int
	TimedOut     bool
	Canceled     bool
	Backgrounded bool
	SessionID    string
	Truncation   *TruncationResult
}

type BashTool struct {
	Box   sandbox.Sandbox
	CwdFn func() string
}

func (b *BashTool) Name() string { return "bash" }

func (b *BashTool) Description() string {
	return fmt.Sprintf(
		"Execute a bash command in the current working directory. Returns stdout and stderr. "+
			"Output is truncated to last %d lines or %dKB (whichever is hit first). "+
			"Waits up to yieldMs (default %ds) for the command to finish; if it's still "+
			"running it is backgrounded as a session (use the process tool for follow-up) "+
			"rather than blocked on or killed. timeout is the hard-kill deadline (default %dm).",
		MaxOutputLines, MaxOutputBytes/1024,
		int(DefaultYield/time.Second), int(DefaultHardTimeout/time.Minute),
	)
}

func (b *BashTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command":    map[string]interface{}{"type": "string", "description": "Bash command to execute"},
			"timeout":    map[string]interface{}{"type": "number", "description": "Hard-kill deadline in seconds (optional; default 30 minutes)"},
			"pty":        map[string]interface{}{"type": "boolean", "description": "Spawn through a pseudo-terminal for TTY-requiring programs (TUIs, coding agents). Falls back to a pipe with a warning if the PTY spawn fails."},
			"yieldMs":    map[string]interface{}{"type": "number", "description": "Milliseconds to wait synchronously before backgrounding (optional; default 10000)"},
			"background": map[string]interface{}{"type": "boolean", "description": "Background the command immediately without waiting"},
		},
		"required": []string{"command"},
	}
}

func (b *BashTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) ([]message.Content, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	var timeout time.Duration
	if t, ok := args["timeout"].(float64); ok && t > 0 {
		timeout = time.Duration(t * float64(time.Second))
	}
	usePTY, _ := args["pty"].(bool)
	var yield time.Duration
	if y, ok := args["yieldMs"].(float64); ok && y >= 0 {
		yield = time.Duration(y * float64(time.Millisecond))
	}
	background, _ := args["background"].(bool)

	res, err := b.run(ctx, BashRequest{
		Command:    command,
		Timeout:    timeout,
		PTY:        usePTY,
		YieldMs:    yield,
		Background: background,
	}, onOutput)
	if err != nil {
		return nil, err
	}
	return []message.Content{message.TextContent(res.Output)}, nil
}

func (b *BashTool) Run(ctx context.Context, req BashRequest) (BashResult, error) {
	return b.run(ctx, req, nil)
}

func (b *BashTool) run(ctx context.Context, req BashRequest, onOutput OnOutput) (BashResult, error) {
	if req.Command == "" {
		return BashResult{}, fmt.Errorf("command is required")
	}
	yield := req.YieldMs
	if yield == 0 && !req.Background {
		yield = DefaultYield
	}
	hard := req.Timeout
	if hard == 0 {
		hard = DefaultHardTimeout
	}
	sess, err := b.Box.Sessions().Start(sandbox.ExecRequest{
		Command: req.Command,
		Cwd:     cwdOf(b.CwdFn),
		Timeout: hard,
		PTY:     req.PTY,
		OnChunk: onOutput,
	})
	if err != nil {
		return BashResult{}, err
	}
	if req.Background {
		return b.backgrounded(sess), nil
	}
	select {
	case <-sess.Done():
		return b.finished(sess, false, hard), nil
	case <-time.After(yield):
		return b.backgrounded(sess), nil
	case <-ctx.Done():
		sess.Kill()
		<-sess.Done()
		return b.finished(sess, true, hard), nil
	}
}

func (b *BashTool) finished(sess sandbox.Session, canceled bool, timeout time.Duration) BashResult {
	res := sess.Wait()
	b.Box.Sessions().Remove(sess.ID())
	return formatResult(string(sess.Log()), res.ExitCode, res.TimedOut, canceled || res.Canceled, timeout)
}

func (b *BashTool) backgrounded(sess sandbox.Session) BashResult {
	msg := fmt.Sprintf("Command still running (session %s). Use the process tool for follow-up.", sess.ID())
	if out := sanitizeOutput(string(sess.Log())); out != "" {
		trunc := TruncateTail(out, TruncationOptions{})
		msg = trunc.Content + "\n\n" + msg
	}
	return BashResult{Output: msg, Backgrounded: true, SessionID: sess.ID()}
}

func formatResult(raw string, exitCode int, timedOut, canceled bool, timeout time.Duration) BashResult {
	raw = sanitizeOutput(raw)
	raw = truncateMiddle(raw, maxOutputChars())
	trunc := TruncateTail(raw, TruncationOptions{})
	output := trunc.Content
	if output == "" {
		output = "(no output)"
	}
	if trunc.Truncated {
		output += fmt.Sprintf("\n\n[Output truncated to last %d lines / %dKB]", MaxOutputLines, MaxOutputBytes/1024)
	}
	switch {
	case timedOut:
		output += fmt.Sprintf("\n\nCommand timed out after %s", timeout)
	case canceled:
		output += "\n\nCommand aborted"
	case exitCode != 0:
		output += fmt.Sprintf("\n\nCommand exited with code %d", exitCode)
	}
	return BashResult{
		Output:     output,
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		Canceled:   canceled,
		Truncation: &trunc,
	}
}

func sanitizeOutput(s string) string {
	s = stripANSI(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Cs, r):
			return -1
		default:
			return r
		}
	}, s)
}

func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case '[':
			i++
			for i < len(s) && s[i] >= 0x20 && s[i] < 0x40 {
				i++
			}
			if i < len(s) {
				i++
			}
		case ']':
			i++
			for i < len(s) {
				if s[i] == 0x07 {
					i++
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return b.String()
}
