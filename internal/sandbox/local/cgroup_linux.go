//go:build linux

package local

import "syscall"

func intoCgroup(a *syscall.SysProcAttr, fd int) *syscall.SysProcAttr {
	if fd >= 0 {
		a.UseCgroupFD, a.CgroupFD = true, fd
	}
	return a
}
