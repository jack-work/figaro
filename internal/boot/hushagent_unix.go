//go:build !windows

package boot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func awaitAgentGone(runtimeDir string, within time.Duration) error {
	pid := filepath.Join(runtimeDir, "agent.pid")
	f, err := os.Open(pid)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	for deadline := time.Now().Add(within); ; time.Sleep(20 * time.Millisecond) {
		if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hush: the old agent still holds %s after %s; it is draining, and the next renewal tries again", pid, within)
		}
	}
}
