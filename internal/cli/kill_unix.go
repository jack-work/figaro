//go:build !windows

package cli

import "syscall"

func killPid(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// pidAlive reports whether pid still exists. Signal 0 outlives every file the
// daemon removes on its way out.
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
