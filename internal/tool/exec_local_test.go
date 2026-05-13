package tool

import (
	"context"
	"os"
	"strings"
	"testing"
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
