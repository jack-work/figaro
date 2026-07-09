//go:build !windows

package cli

import "syscall"

func killPid(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
