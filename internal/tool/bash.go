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

// BashRequest is the typed input to the bash tool.
type BashRequest struct {
	Command string
	// Timeout is the wall-clock limit. Zero = no timeout.
	Timeout time.Duration
}

// BashResult is the tool's output and exit metadata.
type BashResult struct {
	Output     string
	ExitCode   int
	TimedOut   bool
	Canceled   bool
	Truncation *TruncationResult
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
}

// NewBashTool constructs a BashTool with a static cwd and the default
// (unsanitized) local executor. Kept for tests and trivial callers;
// production wiring should use NewBashToolWith.
func NewBashTool(cwd string) *BashTool {
	return &BashTool{
		CwdFn:    func() string { return cwd },
		Executor: NewLocalExecutor(),
	}
}

// NewBashToolWith constructs a BashTool with an explicit cwd function
// and executor.
func NewBashToolWith(cwdFn func() string, executor Executor) *BashTool {
	if executor == nil {
		executor = NewLocalExecutor()
	}
	return &BashTool{CwdFn: cwdFn, Executor: executor}
}

func (b *BashTool) Name() string { return "bash" }
func (b *BashTool) Description() string {
	return fmt.Sprintf(
		"Execute a bash command in the current working directory. Returns stdout and stderr. "+
			"Output is truncated to last %d lines or %dKB (whichever is hit first). "+
			"Optionally provide a timeout in seconds.",
		MaxOutputLines, MaxOutputBytes/1024,
	)
}

func (b *BashTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Bash command to execute"},
			"timeout": map[string]interface{}{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"},
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

	res, err := b.run(ctx, BashRequest{Command: command, Timeout: timeout}, onOutput)
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

	sw := &streamWriter{onOutput: onOutput}
	execReq := ExecRequest{
		Command: req.Command,
		Cwd:     cwd,
		Timeout: req.Timeout,
	}
	res, err := b.Executor.Execute(ctx, execReq, func(chunk []byte) {
		sw.Write(chunk)
	})
	if err != nil {
		return BashResult{}, err
	}

	return b.formatResult(sanitizeOutput(sw.String()), res.ExitCode, res.TimedOut, res.Canceled, req.Timeout)
}

func (b *BashTool) formatResult(raw string, exitCode int, timedOut, canceled bool, timeout time.Duration) (BashResult, error) {
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
