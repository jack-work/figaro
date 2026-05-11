//go:build !windows

package cli

import "syscall"

// detachAttr returns SysProcAttr for detaching from the terminal.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
