//go:build !windows

package activation

import "syscall"

func closeOnExec(fd int) { syscall.CloseOnExec(fd) }
