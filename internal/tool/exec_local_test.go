package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
)

// TestLocalExecutor_EnvSanitizer_StripsDaemonVars verifies that the
// default daemon-env denylist actually keeps _FIGARO_DAEMON (and the
// HUSH_* family) from leaking into the bash child. This is the
// regression test for the reentrant-figaro-hang bug: an outer agent
// would spawn `figaro version`, which inherited _FIGARO_DAEMON=1 from
// the angelus, re-entered daemon mode, and hung on accept().
func TestLocalExecutor_EnvSanitizer_StripsDaemonVars(t *testing.T) {
	t.Setenv("_FIGARO_DAEMON", "1")
	t.Setenv("HUSH_AGENT_CHILD", "1")
	t.Setenv("HUSH_MANAGED_STATE_DIR", "/somewhere")
	t.Setenv("FIGARO_KEEP_THIS", "keep")

	exe := NewLocalExecutor(NewDefaultEnvSanitizer())

	var captured strings.Builder
	res, err := exe.Execute(context.Background(),
		ExecRequest{Command: "env"},
		func(chunk []byte) { captured.Write(chunk) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d, output: %s", res.ExitCode, captured.String())
	}

	output := captured.String()
	denied := []string{
		"_FIGARO_DAEMON=",
		"HUSH_AGENT_CHILD=",
		"HUSH_MANAGED_STATE_DIR=",
	}
	for _, d := range denied {
		if strings.Contains(output, d) {
			t.Errorf("child env still contains %q\nfull env:\n%s", d, output)
		}
	}
	if !strings.Contains(output, "FIGARO_KEEP_THIS=keep") {
		t.Errorf("non-denied var was stripped; output:\n%s", output)
	}
}

// TestLocalExecutor_NoSanitizer_PassesEverything is the negative
// control: without the sanitizer, _FIGARO_DAEMON does pass through.
func TestLocalExecutor_NoSanitizer_PassesEverything(t *testing.T) {
	t.Setenv("_FIGARO_DAEMON", "1")

	exe := NewLocalExecutor()
	var captured strings.Builder
	_, err := exe.Execute(context.Background(),
		ExecRequest{Command: "env"},
		func(chunk []byte) { captured.Write(chunk) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(captured.String(), "_FIGARO_DAEMON=1") {
		t.Fatal("expected unsanitized executor to leak _FIGARO_DAEMON, but it didn't")
	}
}

func TestSanitizeOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nul stripped", "a\x00b", "ab"},
		{"tab newline cr preserved", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"multibyte rune preserved", "café — 日本語 🎉", "café — 日本語 🎉"},
		{"del stripped", "a\x7fb", "ab"},
		{"format char stripped", "a​b", "ab"},
		{"c0 controls stripped", "\x01\x1fok", "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeOutput(tt.in); got != tt.want {
				t.Errorf("sanitizeOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		max       int
		truncated bool
	}{
		{"under cap passthrough", "hello world", 100, false},
		{"at cap passthrough", strings.Repeat("a", 50), 50, false},
		{"over cap middle truncation", "HEAD" + strings.Repeat("x", 1000) + "TAIL", 20, true},
		{"disabled by zero cap", strings.Repeat("a", 1000), 0, false},
		{"multibyte rune safe", strings.Repeat("世", 100), 20, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.input, tt.max)

			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}

			if !tt.truncated {
				if got != tt.input {
					t.Fatalf("under-cap input was altered:\n got %q\nwant %q", got, tt.input)
				}
				if strings.Contains(got, "truncated") {
					t.Fatalf("under-cap input got a truncation marker: %q", got)
				}
				return
			}

			if got == tt.input {
				t.Fatalf("over-cap input was passed through unchanged")
			}

			// Budget preserved: kept runes == max (marker is extra).
			dropped := utf8.RuneCountInString(tt.input) - tt.max
			marker := fmt.Sprintf("[%d characters truncated]", dropped)
			if !strings.Contains(got, marker) {
				t.Fatalf("missing/incorrect drop count, want marker %q in:\n%s", marker, got)
			}
		})
	}
}

func TestTruncateMiddle_PreservesHeadAndTail(t *testing.T) {
	in := "HEAD" + strings.Repeat("x", 1000) + "TAIL"
	got := truncateMiddle(in, 20)

	if !strings.HasPrefix(got, "HEAD") {
		t.Errorf("head not preserved: %q", got)
	}
	if !strings.HasSuffix(got, "TAIL") {
		t.Errorf("tail not preserved: %q", got)
	}
}

func TestMaxOutputChars_EnvOverride(t *testing.T) {
	if got := maxOutputChars(); got != MaxOutputChars {
		t.Fatalf("default cap = %d, want %d", got, MaxOutputChars)
	}

	t.Setenv("FIGARO_BASH_MAX_OUTPUT_CHARS", "500")
	if got := maxOutputChars(); got != 500 {
		t.Fatalf("override cap = %d, want 500", got)
	}

	t.Setenv("FIGARO_BASH_MAX_OUTPUT_CHARS", "garbage")
	if got := maxOutputChars(); got != MaxOutputChars {
		t.Fatalf("invalid override should fall back to %d, got %d", MaxOutputChars, got)
	}
}

// TestLocalExecutor_TimeoutDrainsLateOutput proves the post-SIGKILL
// grace window: the command emits output and then spawns a setsid
// grandchild that escapes the process group and keeps the stdio pipe
// open, so cmd.Wait never returns on its own. Without the grace window
// the executor would block forever on <-done; with it, it gives up
// after killGraceWindow, reports TimedOut, and still captures the
// output that drained before the kill.
func TestLocalExecutor_TimeoutDrainsLateOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setsid process-group escape is Unix-specific")
	}
	exe := NewLocalExecutor()

	type result struct {
		res ExecResult
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var mu sync.Mutex
		var captured strings.Builder
		res, err := exe.Execute(context.Background(),
			ExecRequest{
				Command: "echo DRAINED; setsid sleep 30 & sleep 30",
				Timeout: 200 * time.Millisecond,
			},
			func(chunk []byte) {
				mu.Lock()
				captured.Write(chunk)
				mu.Unlock()
			})
		mu.Lock()
		out := captured.String()
		mu.Unlock()
		ch <- result{res, out, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("execute: %v", r.err)
		}
		if !r.res.TimedOut {
			t.Errorf("expected TimedOut, got %+v", r.res)
		}
		if !strings.Contains(r.out, "DRAINED") {
			t.Errorf("pre-kill output not drained; got %q", r.out)
		}
	case <-time.After(killGraceWindow + 5*time.Second):
		t.Fatal("Execute did not return within the grace window; SIGKILL drain blocked")
	}
}

// TestLocalExecutor_PTY covers the pseudo-terminal path: output is
// captured like the pipe path, the child sees a real TTY, and when the
// PTY spawn is forced to fail the executor warns and falls back to the
// pipe instead of erroring.
func TestLocalExecutor_PTY(t *testing.T) {
	// ConPTY is unavailable in some Windows CI / terminal environments;
	// the non-forceFail cases need a working PTY.
	ptyWorks := true
	if exe := NewLocalExecutor(); exe.ptyStart == nil {
		func() {
			defer func() { recover() }()
			cmd := exec.Command("echo", "probe")
			if _, err := pty.Start(cmd); err != nil {
				ptyWorks = false
			} else {
				cmd.Wait()
			}
		}()
	}
	tests := []struct {
		name      string
		command   string
		forceFail bool
		wantOut   string
		wantWarn  bool
	}{
		{
			name:    "captures output",
			command: "echo hello-pty",
			wantOut: "hello-pty",
		},
		{
			name:    "child reports a tty",
			command: "test -t 1 && echo IS_TTY || echo NOT_TTY",
			wantOut: "IS_TTY",
		},
		{
			name:      "fallback warns and still runs",
			command:   "echo after-fallback",
			forceFail: true,
			wantOut:   "after-fallback",
			wantWarn:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.forceFail && !ptyWorks {
				t.Skip("ConPTY unavailable")
			}
			exe := NewLocalExecutor()
			if tt.forceFail {
				exe.ptyStart = func(*exec.Cmd) (*os.File, error) {
					return nil, fmt.Errorf("forced pty failure")
				}
			}

			var out strings.Builder
			res, err := exe.Execute(context.Background(),
				ExecRequest{Command: tt.command, PTY: true},
				func(chunk []byte) { out.Write(chunk) })
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.ExitCode != 0 {
				t.Fatalf("exit %d, output: %s", res.ExitCode, out.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output %q missing %q", out.String(), tt.wantOut)
			}
			gotWarn := strings.Contains(out.String(), "PTY spawn failed, fell back to pipe")
			if gotWarn != tt.wantWarn {
				t.Errorf("warn=%v, want %v; output: %s", gotWarn, tt.wantWarn, out.String())
			}
		})
	}
}

// TestLocalExecutor_PTY_DSR verifies a child blocking on a cursor-
// position request (DSR, ESC[6n) gets a synthetic reply and completes,
// rather than hanging until the timeout.
func TestLocalExecutor_PTY_DSR(t *testing.T) {
	exe := NewLocalExecutor()
	// In raw mode (as real TUIs use), emit a DSR query then block on a
	// single byte of the reply. Without the synthetic answer this read
	// never returns and the 5s timeout fires.
	cmd := `stty raw; printf '\033[6n'; dd bs=1 count=1 >/dev/null 2>&1; echo DSR_OK`

	var out strings.Builder
	res, err := exe.Execute(context.Background(),
		ExecRequest{Command: cmd, PTY: true, Timeout: 5 * time.Second},
		func(chunk []byte) { out.Write(chunk) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.TimedOut {
		t.Fatalf("child hung waiting for DSR reply; output: %s", out.String())
	}
	if !strings.Contains(out.String(), "DSR_OK") {
		t.Errorf("output %q missing DSR_OK", out.String())
	}
}

// TestCwdResolver_CalledPerInvocation ensures the resolver re-reads
// the source on every call, not once at construction. This is the
// regression test for the figwal symptom: a stale cwd captured at
// agent-construction time persisted across `figaro set system.cwd`.
func TestCwdResolver_CalledPerInvocation(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	current := dir1
	resolver := CwdResolver{Fn: func() string { return current }}

	exe := NewLocalExecutor(resolver)

	run := func() string {
		var out strings.Builder
		_, err := exe.Execute(context.Background(),
			ExecRequest{Command: "pwd"},
			func(chunk []byte) { out.Write(chunk) })
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return strings.TrimSpace(out.String())
	}

	// macOS resolves /tmp -> /private/tmp; Windows bash returns MSYS2
	// paths (/tmp/...) for native paths (C:\...\Temp\...). Compare via
	// os.Stat where possible, fall back to basename match.
	sameDir := func(a, b string) bool {
		ai, errA := os.Stat(a)
		bi, errB := os.Stat(b)
		if errA == nil && errB == nil {
			return os.SameFile(ai, bi)
		}
		return filepath.Base(a) == filepath.Base(b)
	}

	got := run()
	if !sameDir(got, dir1) {
		t.Fatalf("first call: cwd %s != %s", got, dir1)
	}
	current = dir2
	got = run()
	if !sameDir(got, dir2) {
		t.Fatalf("after change: cwd %s != %s", got, dir2)
	}
}
