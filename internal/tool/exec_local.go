package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// LocalExecutor runs commands as direct child processes via exec.Command.
//
// Before exec, every transformer in Transformers is applied in order.
// EnvSanitizer transformers additionally strip their Denylist from the
// inherited os.Environ() base — that step is folded in here rather
// than the transformer chain so a transformer doesn't have to
// materialize a full env slice.
type LocalExecutor struct {
	Transformers []ExecTransformer

	// ptyStart starts cmd through a PTY and returns the master. Nil
	// means pty.Start; tests override it to force a spawn failure and
	// exercise the pipe fallback.
	ptyStart func(*exec.Cmd) (*os.File, error)
}

// NewLocalExecutor builds a LocalExecutor with the supplied transformer
// chain. Pass nil for no transformations.
func NewLocalExecutor(transformers ...ExecTransformer) *LocalExecutor {
	return &LocalExecutor{Transformers: transformers}
}

func (e *LocalExecutor) Execute(ctx context.Context, req ExecRequest, onChunk func([]byte)) (ExecResult, error) {
	for _, t := range e.Transformers {
		req = t.Apply(req)
	}
	if req.Command == "" {
		return ExecResult{}, fmt.Errorf("command is required")
	}

	base := stripDenied(os.Environ(), e.Transformers)
	newCmd := func() *exec.Cmd {
		cmd := exec.Command("bash", "-c", req.Command)
		cmd.Dir = req.Cwd
		cmd.Env = mergeEnv(base, req.Env)
		return cmd
	}

	if req.PTY {
		res, err, ok := e.execPTY(ctx, newCmd(), req, onChunk)
		if ok {
			return res, err
		}
		// PTY spawn failed; warning already emitted via onChunk. Fall
		// through to the unchanged pipe path with a fresh command.
	}

	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	sw := &streamWriter{onOutput: onChunk}
	cmd.Stdout = sw
	cmd.Stderr = sw

	if err := cmd.Start(); err != nil {
		return ExecResult{}, fmt.Errorf("start command: %w", err)
	}

	exitCode, timedOut, canceled, err := waitCmd(ctx, cmd, req.Timeout)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{ExitCode: exitCode, TimedOut: timedOut, Canceled: canceled}, nil
}

// dsrQuery is the "report cursor position" (DSR) escape a TUI emits
// when it wants to know where the cursor is; dsrReport is the synthetic
// answer we feed back so the child doesn't hang waiting for one.
var (
	dsrQuery  = []byte("\x1b[6n")
	dsrReport = []byte("\x1b[1;1R")
)

// execPTY runs cmd through a pseudo-terminal. The ok return is false
// only when the PTY spawn itself fails — in that case a warning has
// been written to onChunk and the caller should retry on the pipe path.
// Once the PTY is up, any subsequent error is returned with ok=true.
func (e *LocalExecutor) execPTY(ctx context.Context, cmd *exec.Cmd, req ExecRequest, onChunk func([]byte)) (ExecResult, error, bool) {
	start := e.ptyStart
	if start == nil {
		start = pty.Start
	}
	ptmx, err := start(cmd)
	if err != nil {
		if onChunk != nil {
			onChunk([]byte(fmt.Sprintf("PTY spawn failed, fell back to pipe: %v\n", err)))
		}
		return ExecResult{}, nil, false
	}
	defer ptmx.Close()

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if bytes.Contains(buf[:n], dsrQuery) {
					ptmx.Write(dsrReport)
				}
				if onChunk != nil {
					chunk := make([]byte, n)
					copy(chunk, buf[:n])
					onChunk(chunk)
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	exitCode, timedOut, canceled, werr := waitCmd(ctx, cmd, req.Timeout)
	ptmx.Close()
	<-copyDone
	if werr != nil {
		return ExecResult{}, werr, true
	}
	return ExecResult{ExitCode: exitCode, TimedOut: timedOut, Canceled: canceled}, nil, true
}

// waitCmd waits for an already-started cmd, enforcing timeout and ctx
// cancellation by killing the process group. A nil error with the
// returned flags is the normal path; a non-nil error is an unexpected
// wait failure (not a non-zero exit).
func waitCmd(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (exitCode int, timedOut, canceled bool, err error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	var waitErr error
	select {
	case waitErr = <-done:
	case <-timeoutCh:
		timedOut = true
		killProcessGroup(cmd)
		<-done
	case <-ctx.Done():
		canceled = true
		killProcessGroup(cmd)
		<-done
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if !timedOut && !canceled {
			return 0, timedOut, canceled, waitErr
		}
	}
	return exitCode, timedOut, canceled, nil
}

// stripDenied removes denylisted keys (gathered from EnvSanitizer
// transformers) from base.
func stripDenied(base []string, transformers []ExecTransformer) []string {
	denied := map[string]struct{}{}
	for _, t := range transformers {
		if s, ok := t.(EnvSanitizer); ok {
			for _, k := range s.Denylist {
				denied[k] = struct{}{}
			}
		}
	}
	if len(denied) == 0 {
		return base
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 {
			if _, drop := denied[kv[:eq]]; drop {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

// mergeEnv returns base with overrides applied. Each entry in overrides
// either replaces a matching KEY= entry in base or is appended.
func mergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	idx := make(map[string]int, len(base))
	for i, kv := range base {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			idx[kv[:eq]] = i
		}
	}
	out := append([]string(nil), base...)
	for _, kv := range overrides {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if i, ok := idx[key]; ok {
			out[i] = kv
		} else {
			out = append(out, kv)
			idx[key] = len(out) - 1
		}
	}
	return out
}

// DefaultDaemonEnvDenylist is the set of environment variables that
// must not leak from a figaro daemon (angelus / agent) into a child
// process spawned by a tool. Inheriting any of these would cause the
// child figaro to re-enter daemon mode and silently hijack the live
// angelus's socket.
var DefaultDaemonEnvDenylist = []string{
	"_FIGARO_DAEMON",
	"HUSH_AGENT_CHILD",
	"HUSH_MANAGED_CONFIG_DIR",
	"HUSH_MANAGED_STATE_DIR",
	"HUSH_MANAGED_RUNTIME_DIR",
}

// EnvSanitizer strips Denylist keys from the inherited os.Environ()
// base. It's a marker-style transformer: LocalExecutor pulls the
// Denylist out and applies the strip before merging req.Env.
type EnvSanitizer struct {
	Denylist []string
}

// NewDefaultEnvSanitizer returns an EnvSanitizer with the figaro
// daemon-mode denylist.
func NewDefaultEnvSanitizer() EnvSanitizer {
	return EnvSanitizer{Denylist: DefaultDaemonEnvDenylist}
}

// Apply is a no-op: the strip happens in LocalExecutor.stripDenied.
func (s EnvSanitizer) Apply(req ExecRequest) ExecRequest { return req }

// CwdResolver fills in req.Cwd when it's empty. The Fn is invoked at
// call time so the source (typically the chalkboard) can change
// between invocations.
type CwdResolver struct {
	Fn func() string
}

func (c CwdResolver) Apply(req ExecRequest) ExecRequest {
	if req.Cwd != "" || c.Fn == nil {
		return req
	}
	req.Cwd = c.Fn()
	return req
}
