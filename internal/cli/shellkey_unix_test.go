//go:build !windows

package cli

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Stable across a fork -- the point of the change. A child's parent is not
// the shell, but its session is.
func TestShellKeyIsStableAcrossAFork(t *testing.T) {
	out, err := exec.Command("sh", "-c", "ps -o sid= -p $$").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Skipf("unparseable sid %q", out)
	}
	if child != shellKey() {
		t.Errorf("child session %d != ours %d", child, shellKey())
	}
}

// The key is a live pid, so kill(pid,0) reaping and start-time reuse
// detection keep working unmodified.
func TestShellKeyNamesALiveProcess(t *testing.T) {
	key := shellKey()
	if key <= 0 {
		t.Fatalf("shellKey() = %d", key)
	}
	if err := unix.Kill(key, 0); err != nil {
		t.Errorf("shellKey() = %d names no live process: %v", key, err)
	}
}
