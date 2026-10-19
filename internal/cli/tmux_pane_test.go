//go:build !windows

package cli

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// awaitPaneGone holds a test until the process group a tmux pane started is
// empty. kill-server returns before the pane's processes exit, and a pane that
// writes into the test's temp dir would otherwise race t.TempDir's cleanup.
func awaitPaneGone(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for unix.Kill(-pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Errorf("the pane's process group %d outlived the tmux server", pid)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
