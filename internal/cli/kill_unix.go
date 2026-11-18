//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

func killPid(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
