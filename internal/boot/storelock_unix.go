//go:build !windows

package boot

import (
	"os"
	"syscall"
)

func TryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
