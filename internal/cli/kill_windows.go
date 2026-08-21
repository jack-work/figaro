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

// pidAlive reports whether pid still exists. Windows has no signal 0, so ask
// for the exit code: a live process reports STILL_ACTIVE (259).
func pidAlive(pid int) bool {
	const stillActive = 259
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
