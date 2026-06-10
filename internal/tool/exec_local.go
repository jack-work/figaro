package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
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
}

// NewLocalExecutor builds a LocalExecutor with the supplied transformer
// chain. Pass nil for no transformations.
func NewLocalExecutor(transformers ...ExecTransformer) *LocalExecutor {
	return &LocalExecutor{Transformers: transformers}
}

// buildCmd applies the transformer chain and assembles the *exec.Cmd
// (its own process group, sanitized+merged env, cwd) ready to Start.
func (e *LocalExecutor) buildCmd(req ExecRequest) (*exec.Cmd, error) {
	for _, t := range e.Transformers {
		req = t.Apply(req)
	}
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	base := stripDenied(os.Environ(), e.Transformers)
	cmd := exec.Command("bash", "-c", req.Command)
	cmd.Dir = req.Cwd
	cmd.Env = mergeEnv(base, req.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}

func (e *LocalExecutor) Execute(ctx context.Context, req ExecRequest, onChunk func([]byte)) (ExecResult, error) {
	cmd, err := e.buildCmd(req)
	if err != nil {
		return ExecResult{}, err
	}

	sw := &streamWriter{onOutput: onChunk}
	cmd.Stdout = sw
	cmd.Stderr = sw

	if err := cmd.Start(); err != nil {
		return ExecResult{}, fmt.Errorf("start command: %w", err)
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

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if !timedOut && !canceled {
			return ExecResult{}, waitErr
		}
	}

	return ExecResult{
		ExitCode: exitCode,
		TimedOut: timedOut,
		Canceled: canceled,
	}, nil
}

// Start launches the command and returns a Process whose lifetime is
// independent of any context — the caller (a session) owns when to
// wait, signal, or kill it. Output streams to onChunk for as long as
// the process runs.
func (e *LocalExecutor) Start(req ExecRequest, onChunk func([]byte)) (Process, error) {
	cmd, err := e.buildCmd(req)
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	sw := &streamWriter{onOutput: onChunk}
	cmd.Stdout = sw
	cmd.Stderr = sw
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}
	p := &localProcess{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		waitErr := cmd.Wait()
		p.exitCode = exitCode(waitErr)
		close(p.done)
	}()
	return p, nil
}

// exitCode extracts a process exit code from cmd.Wait's error.
func exitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// localProcess is a LocalExecutor-started command. cmd.Wait runs once
// in a goroutine started by Start; Wait here just blocks on done.
type localProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	exitCode int
}

func (p *localProcess) Pid() int { return p.cmd.Process.Pid }

func (p *localProcess) Wait() int {
	<-p.done
	return p.exitCode
}

func (p *localProcess) Signal(sig syscall.Signal) error {
	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

func (p *localProcess) Write(b []byte) (int, error) { return p.stdin.Write(b) }

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
