//go:build !windows

package boot

import "syscall"

func closeOnExec(fd int) { syscall.CloseOnExec(fd) }
