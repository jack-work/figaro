package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// BashRequest is the typed input to the bash tool.
type BashRequest struct {
	Command string
	// Timeout is the wall-clock limit for the command. Zero means no
	// timeout (the context still applies).
	Timeout time.Duration
}

// BashResult bundles the final output string with metadata about how
// the command finished. Output is the (possibly truncated) captured
// stdout+stderr and is what the model sees.
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
type BashTool struct {
	Cwd string
}

// NewBashTool constructs a BashTool bound to cwd.
func NewBashTool(cwd string) *BashTool { return &BashTool{Cwd: cwd} }

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

// Run is the typed Go API. Non-zero exit codes are not returned as an
// error — they're surfaced in the BashResult.ExitCode field and
// reflected in the formatted Output string.
func (b *BashTool) Run(ctx context.Context, req BashRequest) (BashResult, error) {
	return b.run(ctx, req, nil)
}

func (b *BashTool) run(ctx context.Context, req BashRequest, onOutput OnOutput) (BashResult, error) {
	if req.Command == "" {
		return BashResult{}, fmt.Errorf("command is required")
	}

	// We manage killing ourselves rather than using exec.CommandContext,
	// because we need to kill the entire process group (Setpgid).
	cmd := exec.Command("bash", "-c", req.Command)
	cmd.Dir = b.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	sw := &streamWriter{onOutput: onOutput}
	cmd.Stdout = sw
	cmd.Stderr = sw

	if err := cmd.Start(); err != nil {
		return BashResult{}, fmt.Errorf("start command: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeoutCh <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	var timedOut, canceled bool
	select {
	case err := <-done:
		return b.formatResult(sw.String(), err, false, false, req.Timeout)
	case <-timeoutCh:
		timedOut = true
	case <-ctx.Done():
		canceled = true
	}

	killProcessGroup(cmd)
	<-done
	return b.formatResult(sw.String(), nil, timedOut, canceled, req.Timeout)
}

func (b *BashTool) formatResult(raw string, execErr error, timedOut, canceled bool, timeout time.Duration) (BashResult, error) {
	trunc := TruncateTail(raw, TruncationOptions{})
	output := trunc.Content
	if output == "" {
		output = "(no output)"
	}

	if trunc.Truncated {
		output += fmt.Sprintf("\n\n[Output truncated to last %d lines / %dKB]", MaxOutputLines, MaxOutputBytes/1024)
	}

	exitCode := 0
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return BashResult{}, execErr
		}
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

// streamWriter captures all output and optionally streams chunks to a callback.
type streamWriter struct {
	buf      bytes.Buffer
	onOutput OnOutput
}

func (w *streamWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.onOutput != nil && n > 0 {
		chunk := make([]byte, n)
		copy(chunk, p[:n])
		w.onOutput(chunk)
	}
	return n, err
}

func (w *streamWriter) String() string { return w.buf.String() }
