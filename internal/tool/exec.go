package tool

import (
	"context"
	"syscall"
	"time"
)

// ExecRequest is a host-agnostic shell-command invocation.
//
// The Executor decides how to run it; transformers may rewrite the
// request before execution (env sanitization, cwd resolution, path
// remapping for remote hosts, etc.).
type ExecRequest struct {
	// Command is the bash -c argument.
	Command string

	// Cwd is the requested working directory. Empty means "let the
	// executor pick" — typically the host default for the current
	// agent.
	Cwd string

	// Env is explicit env additions/overrides as KEY=VAL strings.
	// The executor decides how to merge them with the host
	// environment.
	Env []string

	// Timeout is the wall-clock limit. Zero means no timeout.
	Timeout time.Duration

	// PTY requests the command be spawned through a pseudo-terminal so
	// TTY-requiring programs (TUIs, coding agents) behave. If the PTY
	// spawn fails, the executor falls back to a plain pipe and prepends
	// a warning to the output rather than erroring.
	PTY bool
}

// ExecResult is what an Executor returns.
//
// Output is streamed via the onChunk callback passed to Execute; this
// struct only carries terminal metadata.
type ExecResult struct {
	ExitCode int
	TimedOut bool
	Canceled bool
}

// Executor runs an ExecRequest. The local implementation runs via
// exec.Command; future implementations may dial SSH, exec into a
// container, or hand off to a remote agent.
type Executor interface {
	Execute(ctx context.Context, req ExecRequest, onChunk func([]byte)) (ExecResult, error)
}

// Process is a started command whose lifetime is decoupled from the
// call that launched it. Output is streamed to the onChunk callback
// passed to Start; this handle carries control over the live process.
type Process interface {
	// Pid is the process-group leader's pid.
	Pid() int
	// Wait blocks until the process exits and returns its exit code.
	// Safe to call once; the result is delivered to a single caller.
	Wait() int
	// Signal sends sig to the whole process group.
	Signal(sig syscall.Signal) error
	// Write feeds bytes to the process's stdin.
	Write(p []byte) (int, error)
}

// BackgroundExecutor starts a Process without waiting for it. An
// Executor that also implements this can back yield-to-background exec
// sessions; one that doesn't falls back to blocking Execute.
type BackgroundExecutor interface {
	Start(req ExecRequest, onChunk func([]byte)) (Process, error)
}

// ExecTransformer rewrites a request before the executor runs it.
//
// Transformers are applied in order. Typical uses: strip daemon-mode
// env vars before they leak into children; resolve an empty Cwd from
// a live source like the chalkboard; remap host paths for an SSH
// executor.
type ExecTransformer interface {
	Apply(req ExecRequest) ExecRequest
}
