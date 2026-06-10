package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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

// TestLocalExecutor_PTY covers the pseudo-terminal path: output is
// captured like the pipe path, the child sees a real TTY, and when the
// PTY spawn is forced to fail the executor warns and falls back to the
// pipe instead of erroring.
func TestLocalExecutor_PTY(t *testing.T) {
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

	// macOS resolves /tmp -> /private/tmp; compare via os.Stat
	// equivalence rather than string equality.
	sameDir := func(a, b string) bool {
		ai, errA := os.Stat(a)
		bi, errB := os.Stat(b)
		return errA == nil && errB == nil && os.SameFile(ai, bi)
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
