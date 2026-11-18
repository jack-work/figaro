//go:build !windows

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func stopPidCaptured(t *testing.T, pid int, force bool) (exited bool, afterExitRan bool, errs string, took time.Duration) {
	t.Helper()
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	var buf bytes.Buffer
	prev := stderrw
	stderrw = &buf
	t.Cleanup(func() { stderrw = prev })
	start := time.Now()
	_, exited = captureExit(t, func() { stopPid(pid, force, func() { afterExitRan = true }) })
	return exited, afterExitRan, buf.String(), time.Since(start)
}

// A recycled pid that now belongs to another uid answers EPERM; stop must
// refuse at once instead of waiting out the grace period.
func TestStopPidRefusesAnotherUsersPidAtOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs non-root EPERM on pid 1")
	}
	// In a pid namespace (the nix build sandbox) pid 1 is this test's own
	// builder: signalling it would kill the build.
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("pid 1 is signalable here (%v); it may be our own ancestor", err)
	}
	for _, force := range []bool{false, true} {
		exited, ran, errs, took := stopPidCaptured(t, 1, force)
		if !exited || ran {
			t.Fatalf("force=%v: exited=%v afterExit=%v, want a refusal; stderr %q", force, exited, ran, errs)
		}
		if took > 2*time.Second {
			t.Errorf("force=%v: refusal took %s, want immediate", force, took)
		}
		if !strings.Contains(errs, "another user") || strings.Contains(errs, "forcefully terminated") {
			t.Errorf("force=%v: stderr %q", force, errs)
		}
	}
}

// An unreaped child stays visible to kill(pid, 0) after SIGKILL, standing in
// for a process stuck in uninterruptible sleep: --force must not claim it.
func TestStopPidForceClaimsTerminationOnlyWhenThePidIsGone(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	exited, ran, errs, _ := stopPidCaptured(t, cmd.Process.Pid, true)
	if !exited || ran {
		t.Fatalf("exited=%v afterExit=%v, want a refusal; stderr %q", exited, ran, errs)
	}
	if strings.Contains(errs, "forcefully terminated") || !strings.Contains(errs, "still alive") {
		t.Errorf("stderr %q", errs)
	}
}
