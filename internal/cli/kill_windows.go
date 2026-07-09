//go:build windows

package cli

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func killPid(pid int, _ syscall.Signal) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
