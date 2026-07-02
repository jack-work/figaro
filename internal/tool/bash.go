package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// Default knobs for the bash tool.
const (
	// DefaultYield is how long the tool waits synchronously for a
	// command to finish before yielding it to a background session.
	DefaultYield = 10 * time.Second
	// DefaultHardTimeout is the wall-clock deadline after which a
	// (possibly backgrounded) command is killed.
	DefaultHardTimeout = 30 * time.Minute
	// defaultScope is the session scope used when no ScopeFn is set.
	defaultScope = "default"
)

// BashRequest is the typed input to the bash tool.
type BashRequest struct {
	Command string
	// Timeout is the hard-kill wall-clock deadline. Zero = default.
	Timeout time.Duration
	// PTY spawns the command through a pseudo-terminal, with graceful
	// fallback to a pipe if the PTY spawn fails.
	PTY bool
	// YieldMs is how long to wait synchronously before backgrounding.
	// Zero = default (unless Background is set).
	YieldMs time.Duration
	// Background, if set, backgrounds the command immediately without
	// any synchronous wait.
	Background bool
}

// BashResult is the tool's output and exit metadata.
type BashResult struct {
	Output       string
	ExitCode     int
	TimedOut     bool
	Canceled     bool
	Backgrounded bool
	SessionID    string
	Truncation   *TruncationResult
}

// Runner is the Go-level interface.
type Runner interface {
	Run(ctx context.Context, req BashRequest) (BashResult, error)
}

// BashTool implements both Runner and the generic Tool interface.
//
// The actual subprocess invocation is delegated to an Executor; the
// tool just translates between figaro's tool-call shape and
// ExecRequest/ExecResult, plus formats the captured output.
type BashTool struct {
	// CwdFn returns the working directory for each invocation.
	// Called at run time so updates to the chalkboard (system.cwd)
	// take effect immediately. If nil or returns "", the executor
	// picks the default.
	CwdFn func() string

	// Executor runs the request. Defaults to a bare LocalExecutor
	// (no transformers, no env sanitization) — agent wiring is
	// expected to supply a properly-configured executor.
	Executor Executor

	// Sessions backs yield-to-background exec. When nil (or the
	// Executor can't start background processes) the tool falls back
	// to blocking execution.
	Sessions *SessionRegistry

	// ScopeFn returns the session scope for each invocation. nil =>
	// defaultScope.
	ScopeFn func() string
}

// NewBashTool constructs a BashTool with a static cwd and the default
// (unsanitized) local executor. Kept for tests and trivial callers;
// production wiring should use NewBashToolWith.
func NewBashTool(cwd string) *BashTool {
	return &BashTool{
		CwdFn:    func() string { return cwd },
		Executor: NewLocalExecutor(),
		Sessions: NewSessionRegistry(DefaultSessionTTL),
	}
}

// NewBashToolWith constructs a BashTool with an explicit cwd function
// and executor. Pass sessions to back yield-to-background exec (and to
// share a registry with the process tool); nil gets a private one.
func NewBashToolWith(cwdFn func() string, executor Executor, sessions *SessionRegistry) *BashTool {
	if executor == nil {
		executor = NewLocalExecutor()
	}
	if sessions == nil {
		sessions = NewSessionRegistry(DefaultSessionTTL)
	}
	return &BashTool{CwdFn: cwdFn, Executor: executor, Sessions: sessions}
}

func (b *BashTool) scope() string {
	if b.ScopeFn != nil {
		if s := b.ScopeFn(); s != "" {
			return s
		}
	}
	return defaultScope
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

// Run is the typed Go API.
func (b *BashTool) Run(ctx context.Context, req BashRequest) (BashResult, error) {
	return b.run(ctx, req, nil)
}

func (b *BashTool) run(ctx context.Context, req BashRequest, onOutput OnOutput) (BashResult, error) {
	if req.Command == "" {
		return BashResult{}, fmt.Errorf("command is required")
	}

	cwd := ""
	if b.CwdFn != nil {
		cwd = b.CwdFn()
	}

	// PTY commands are interactive (TUIs, coding agents) and only the
	// blocking path drives a pseudo-terminal — the background Start path
	// is pipe-only — so PTY never yields to a session.
	be, canBackground := b.Executor.(BackgroundExecutor)
	if canBackground && b.Sessions != nil && !req.PTY {
		return b.runSession(ctx, be, req, cwd, onOutput)
	}
	return b.runBlocking(ctx, req, cwd, onOutput)
}

// runBlocking is the legacy path for executors that can't background:
// run to completion, honoring timeout and ctx cancellation.
func (b *BashTool) runBlocking(ctx context.Context, req BashRequest, cwd string, onOutput OnOutput) (BashResult, error) {
	sw := &streamWriter{onOutput: onOutput}
	execReq := ExecRequest{Command: req.Command, Cwd: cwd, Timeout: req.Timeout, PTY: req.PTY, Env: bashToolEnv()}
	res, err := b.Executor.Execute(ctx, execReq, func(chunk []byte) { sw.Write(chunk) })
	if err != nil {
		return BashResult{}, err
	}
	return b.formatResult(sw.String(), res.ExitCode, res.TimedOut, res.Canceled, req.Timeout)
}

// runSession starts the command as a session, waits up to yieldMs for
// it to finish, and otherwise leaves it backgrounded. The hard timeout
// (and explicit kills) still terminate the process; a tool-call abort
// during the synchronous window kills, but once backgrounded the
// session is decoupled from ctx entirely.
func (b *BashTool) runSession(ctx context.Context, be BackgroundExecutor, req BashRequest, cwd string, onOutput OnOutput) (BashResult, error) {
	yield := req.YieldMs
	if yield == 0 && !req.Background {
		yield = DefaultYield
	}
	hard := req.Timeout
	if hard == 0 {
		hard = DefaultHardTimeout
	}

	scope := b.scope()
	sess := b.Sessions.Create(scope, req.Command)
	sink := func(chunk []byte) {
		sess.writeChunk(chunk)
		if onOutput != nil {
			onOutput(chunk)
		}
	}
	proc, err := be.Start(ExecRequest{Command: req.Command, Cwd: cwd, Env: bashToolEnv()}, sink)
	if err != nil {
		b.Sessions.Remove(scope, sess.ID)
		return BashResult{}, err
	}
	sess.supervise(proc, hard)

	if req.Background {
		return b.backgroundedResult(sess), nil
	}

	select {
	case <-sess.Done():
		info := sess.Info()
		b.Sessions.Remove(scope, sess.ID)
		return b.formatResult(sess.Log(), info.ExitCode, info.State == SessionTimedOut, false, hard)
	case <-time.After(yield):
		return b.backgroundedResult(sess), nil
	case <-ctx.Done():
		sess.Kill()
		<-sess.Done()
		info := sess.Info()
		b.Sessions.Remove(scope, sess.ID)
		return b.formatResult(sess.Log(), info.ExitCode, false, true, hard)
	}
}

// backgroundedResult is the "still running" payload returned when a
// command outlives its synchronous window.
func (b *BashTool) backgroundedResult(sess *ExecSession) BashResult {
	info := sess.Info()
	msg := fmt.Sprintf("Command still running (session %s, pid %d). Use the process tool for follow-up.", info.ID, info.Pid)
	if out := sanitizeOutput(sess.Log()); out != "" {
		trunc := TruncateTail(out, TruncationOptions{})
		msg = trunc.Content + "\n\n" + msg
	}
	return BashResult{Output: msg, Backgrounded: true, SessionID: info.ID}
}

func (b *BashTool) formatResult(raw string, exitCode int, timedOut, canceled bool, timeout time.Duration) (BashResult, error) {
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
	}, nil
}

// killProcessGroup sends SIGKILL to the entire process group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// streamWriter captures output and optionally streams chunks. Writes
// may continue on a stdio-copy goroutine after a timeout's grace window
// elapses, so the buffer is mutex-guarded against a concurrent String().
type streamWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	onOutput OnOutput
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	if w.onOutput != nil && n > 0 {
		chunk := make([]byte, n)
		copy(chunk, p[:n])
		w.onOutput(chunk)
	}
	return n, err
}

func (w *streamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
