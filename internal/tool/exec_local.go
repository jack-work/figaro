package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode"

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

// buildCmd applies the transformer chain and assembles the *exec.Cmd
// (sanitized+merged env, cwd) ready to Start. It deliberately does NOT
// set SysProcAttr: the pipe and background paths want their own process
// group (Setpgid) so the whole tree can be signaled, while the PTY path
// lets pty.Start install Setsid/Setctty instead — the two are mutually
// exclusive, so each caller sets what it needs.
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
	return cmd, nil
}

func (e *LocalExecutor) Execute(ctx context.Context, req ExecRequest, onChunk func([]byte)) (ExecResult, error) {
	cmd, err := e.buildCmd(req)
	if err != nil {
		return ExecResult{}, err
	}

	if req.PTY {
		res, err, ok := e.execPTY(ctx, cmd, req, onChunk)
		if ok {
			return res, err
		}
		// PTY spawn failed; warning already emitted via onChunk. Fall
		// through to the pipe path with a fresh command.
		cmd, err = e.buildCmd(req)
		if err != nil {
			return ExecResult{}, err
		}
	}

	cmd.SysProcAttr = procAttr()
	sw := &streamWriter{onOutput: onChunk}
	cmd.Stdout = sw
	cmd.Stderr = sw

	if err := cmd.Start(); err != nil {
		return ExecResult{}, fmt.Errorf("start command: %w", err)
	}
	afterStart(cmd)

	exitCode, timedOut, canceled, err := waitCmd(ctx, cmd, req.Timeout)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{ExitCode: exitCode, TimedOut: timedOut, Canceled: canceled}, nil
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
	cmd.SysProcAttr = procAttr()
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
	afterStart(cmd)
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
	return signalTree(p.cmd, sig)
}

func (p *localProcess) Write(b []byte) (int, error) { return p.stdin.Write(b) }

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
		waitErr = drainAfterKill(done)
	case <-ctx.Done():
		canceled = true
		killProcessGroup(cmd)
		waitErr = drainAfterKill(done)
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

// sanitizeOutput strips ANSI escape sequences and control runes that
// would poison the conversation log or corrupt the parent's terminal
// rendering (e.g. a subagent figaro emitting cursor movement). ANSI
// CSI/OSC sequences are stripped as complete units; remaining C0
// controls (except tab, newline, CR), DEL, Unicode format characters,
// and surrogate code points are dropped.
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
		case '[': // CSI: ESC [ <params> <terminator>
			i++
			for i < len(s) && s[i] >= 0x20 && s[i] < 0x40 {
				i++
			}
			if i < len(s) {
				i++
			}
		case ']': // OSC: ESC ] ... (BEL or ST)
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
