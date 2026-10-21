// Package sandbox is the contract between the tool runtime and wherever a
// tool's side effects land. Every tool reaches the filesystem and the shell
// through a Sandbox and nothing else; the daemon's credentials never cross it.
package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"time"
)

// ErrOutsideWorkspace is returned by a provider that claims Isolation.FS
// for a path that, after cleaning and symlink resolution, does not lie
// under Spec.Workspace.
var ErrOutsideWorkspace = errors.New("sandbox: path escapes workspace")

// ErrNoSession is returned by Sessions for an unknown or expired id.
var ErrNoSession = errors.New("sandbox: no such session")

// Provider opens sandboxes. Selected per aria by the form key system.sandbox.
type Provider interface {
	Open(ctx context.Context, spec Spec) (Sandbox, error)
}

type Spec struct {
	// Workspace is the one path the box sees read-write, and the root
	// every FS path resolves against. Absolute, on the host.
	Workspace string
	// Env is what every process starts with. A provider that claims
	// Isolation.Env adds nothing from the daemon's environment; one that
	// does not lays Env over what the daemon has.
	Env map[string]string
}

// Sandbox is one opened Spec. Closing it drops the handle; whether running
// sessions outlive it is Isolation.Persistent.
type Sandbox interface {
	Exec(ctx context.Context, req ExecRequest) (Process, error)
	Sessions() Sessions
	FS() FS
	Isolation() Isolation
	Close() error
}

// ExecRequest is one shell command. Output is stdout and stderr merged,
// delivered to OnChunk in order as it is produced.
type ExecRequest struct {
	Command string
	// Cwd is resolved like an FS path; empty means the workspace itself.
	Cwd string
	// Env overrides Spec.Env for this process only.
	Env map[string]string
	// Timeout is the wall-clock limit after which the process group is
	// killed and Result.TimedOut is set. Zero means none.
	Timeout time.Duration
	// PTY spawns through a pseudo-terminal. A provider that cannot falls
	// back to a pipe and reports it through Process.PTY.
	PTY     bool
	OnChunk func([]byte)
}

type Result struct {
	ExitCode int
	TimedOut bool
	Canceled bool
}

// Process is a started command. Cancelling the Exec context, or the
// Timeout, kills the whole process group. Wait may be called any number of
// times.
type Process interface {
	Wait() Result
	Done() <-chan struct{}
	// Write feeds stdin.
	Write(p []byte) (int, error)
	Interrupt() error
	Kill() error
	// PTY reports whether the process is actually on a pseudo-terminal.
	PTY() bool
}

// Sessions holds background processes. Ids are opaque to the caller and
// never a host pid; a session started here is detached from the context
// that started it and ends only by Timeout, Kill, or exit.
type Sessions interface {
	Start(req ExecRequest) (Session, error)
	Get(id string) (Session, error)
	List() []SessionInfo
	// Remove forgets a session, killing it first if it is still running.
	Remove(id string) error
}

// Session is a background Process with a retained transcript.
type Session interface {
	Process
	ID() string
	Info() SessionInfo
	// Poll returns output not yet returned by a previous Poll.
	Poll() []byte
	// Log returns the whole retained transcript; a provider may cap it.
	Log() []byte
}

type SessionState string

const (
	SessionRunning  SessionState = "running"
	SessionKilling  SessionState = "killing"
	SessionExited   SessionState = "exited"
	SessionKilled   SessionState = "killed"
	SessionTimedOut SessionState = "timed_out"
)

type SessionInfo struct {
	ID        string
	Command   string
	State     SessionState
	ExitCode  int
	StartedAt time.Time
	EndedAt   time.Time
}

// FS is the workspace filesystem. A relative path is resolved against
// Spec.Workspace. A provider that claims Isolation.FS refuses, with
// ErrOutsideWorkspace, any path that escapes it by ".." or by being
// absolute elsewhere; one that does not serves any absolute path.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
	Stat(path string) (fs.FileInfo, error)
	MkdirAll(path string, perm fs.FileMode) error
	Rename(oldpath, newpath string) error
	// Update runs fn on the file's current bytes and writes what it
	// returns, serialized with every other Update of the same path.
	Update(path string, fn func(old []byte) ([]byte, error)) error
}

// Isolation is what a provider actually enforces, declared so the form and
// the model can be told the truth. The zero value declares nothing.
type Isolation struct {
	// Provider is the implementation's name: local, uid, box, remote.
	Provider string
	// UID: processes run as a uid other than the daemon's.
	UID bool
	// FS: a process can write to the host only under Spec.Workspace, and
	// FS refuses any path outside it.
	FS bool
	// Env: a process sees exactly Spec.Env.
	Env bool
	// Net: network egress is cut.
	Net bool
	// Secrets: the daemon's config and credential stores are unreachable.
	Secrets bool
	// Persistent: sessions outlive Close and are found again by a later
	// Open for the same aria.
	Persistent bool
}
