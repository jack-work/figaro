//go:build !windows

package cli

import (
	"fmt"
	"strconv"
	"syscall"

	"github.com/jack-work/figaro/internal/boot"
)

func takeInheritedStoreLock(raw string) error {
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 0 {
		return fmt.Errorf("%s=%q is not a file descriptor", storeLockFDEnv, raw)
	}
	path := boot.StoreLockPath()
	var held, want syscall.Stat_t
	if err := syscall.Fstat(fd, &held); err != nil {
		return fmt.Errorf("%s=%d: %v", storeLockFDEnv, fd, err)
	}
	if err := syscall.Stat(path, &want); err != nil {
		return fmt.Errorf("%s=%d is not open on %s: %v", storeLockFDEnv, fd, path, err)
	}
	if held.Dev != want.Dev || held.Ino != want.Ino {
		return fmt.Errorf("%s=%d is not open on %s", storeLockFDEnv, fd, path)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		who, _, _ := boot.StoreLockHolder()
		return fmt.Errorf("%s=%d is open on %s, but the lock is held by %s through another open", storeLockFDEnv, fd, path, who)
	}
	return nil
}
