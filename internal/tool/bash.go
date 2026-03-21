package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Output truncation limits.
const (
	MaxOutputLines = 2000
	MaxOutputBytes = 50 * 1024 // 50KB
)

type Bash struct{ Cwd string }

func (b *Bash) Name() string { return "bash" }
func (b *Bash) Description() string {
	return fmt.Sprintf(
		"Execute a bash command in the current working directory. Returns stdout and stderr. "+
			"Output is truncated to last %d lines or %dKB (whichever is hit first). "+
			"If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.",
		MaxOutputLines, MaxOutputBytes/1024,
	)
}
func (b *Bash) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Bash command to execute"},
			"timeout": map[string]interface{}{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"},
		},
		"required": []string{"command"},
	}
}

func (b *Bash) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) (string, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	// Parse optional timeout.
	var timeout time.Duration
	if t, ok := args["timeout"].(float64); ok && t > 0 {
		timeout = time.Duration(t * float64(time.Second))
	}

	// We manage killing ourselves rather than using exec.CommandContext,
	// because we need to kill the entire process group (Setpgid).
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = b.Cwd
	// Own process group so we can kill the entire tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// streamWriter captures all output and optionally streams chunks
	// to the onOutput callback as they arrive.
	sw := &streamWriter{onOutput: onOutput}
	cmd.Stdout = sw
	cmd.Stderr = sw

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}

	// Wait for completion, timeout, or context cancellation.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timedOut, canceled bool

	// Build a timeout channel if needed.
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case err := <-done:
		return b.formatResult(sw.String(), err, timedOut, canceled, timeout)
	case <-timeoutCh:
		timedOut = true
	case <-ctx.Done():
		canceled = true
	}

	// Kill the entire process group.
	killProcessGroup(cmd)

	// Wait for the process to actually exit (so pipes close).
	<-done

	return b.formatResult(sw.String(), nil, timedOut, canceled, timeout)
}

// killProcessGroup sends SIGKILL to the entire process group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (b *Bash) formatResult(output string, err error, timedOut, canceled bool, timeout time.Duration) (string, error) {
	output, truncated := truncateTail(output)

	if output == "" {
		output = "(no output)"
	}

	if truncated {
		output += fmt.Sprintf("\n\n[Output truncated to last %d lines / %dKB]", MaxOutputLines, MaxOutputBytes/1024)
	}

	if timedOut {
		return fmt.Sprintf("%s\n\nCommand timed out after %s", output, timeout), nil
	}
	if canceled {
		return fmt.Sprintf("%s\n\nCommand aborted", output), nil
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("%s\n\nCommand exited with code %d", output, exitErr.ExitCode()), nil
		}
		return "", err
	}
	return output, nil
}

// streamWriter captures all output and optionally streams chunks to a callback.
type streamWriter struct {
	buf      bytes.Buffer
	onOutput OnOutput
}

func (w *streamWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.onOutput != nil && n > 0 {
		// Send a copy so the callback can safely hold the reference.
		chunk := make([]byte, n)
		copy(chunk, p[:n])
		w.onOutput(chunk)
	}
	return n, err
}

func (w *streamWriter) String() string {
	return w.buf.String()
}

// truncateTail keeps the last MaxOutputLines / MaxOutputBytes of output.
// Returns the truncated string and whether truncation occurred.
func truncateTail(output string) (string, bool) {
	// Check byte limit first.
	if len(output) > MaxOutputBytes {
		output = output[len(output)-MaxOutputBytes:]
		// Find the first newline to avoid a partial first line.
		if idx := strings.Index(output, "\n"); idx >= 0 {
			output = output[idx+1:]
		}
		return output, true
	}

	// Check line limit.
	lines := strings.Split(output, "\n")
	if len(lines) > MaxOutputLines {
		lines = lines[len(lines)-MaxOutputLines:]
		return strings.Join(lines, "\n"), true
	}

	return output, false
}
