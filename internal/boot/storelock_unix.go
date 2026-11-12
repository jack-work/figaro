//go:build !windows

package boot

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
)

func TryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func processCommand(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	return string(bytes.ReplaceAll(bytes.TrimRight(raw, "\x00"), []byte{0}, []byte{' '}))
}
