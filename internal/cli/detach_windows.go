//go:build windows

package cli

import "syscall"

// detachAttr returns SysProcAttr for detaching from the console.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
