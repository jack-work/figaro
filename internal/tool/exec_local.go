package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode"
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

func (e *LocalExecutor) Execute(ctx context.Context, req ExecRequest, onChunk func([]byte)) (ExecResult, error) {
	for _, t := range e.Transformers {
		req = t.Apply(req)
	}
	if req.Command == "" {
		return ExecResult{}, fmt.Errorf("command is required")
	}

	base := stripDenied(os.Environ(), e.Transformers)

	cmd := exec.Command("bash", "-c", req.Command)
	cmd.Dir = req.Cwd
	cmd.Env = mergeEnv(base, req.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		waitErr = drainAfterKill(done)
	case <-ctx.Done():
		canceled = true
		killProcessGroup(cmd)
		waitErr = drainAfterKill(done)
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

// killGraceWindow is how long, after SIGKILL, we wait for the process's
// own Wait to return so its buffered stdout/stderr drains naturally into
// the aggregated output. If it elapses (e.g. a grandchild escaped the
// process group and holds the stdio pipe open), we synthesize the
// outcome rather than block forever.
const killGraceWindow = time.Second

// drainAfterKill waits up to killGraceWindow for done. It returns the
// process's wait error if it arrives in time, otherwise nil.
func drainAfterKill(done <-chan error) error {
	timer := time.NewTimer(killGraceWindow)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return nil
	}
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

// sanitizeOutput strips control and format runes that would poison the
// conversation log: C0 controls (except tab, newline, carriage return),
// DEL, Unicode format characters, and surrogate code points. Printable
// UTF-8 is left untouched.
func sanitizeOutput(s string) string {
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
