//go:build linux

package local

import "syscall"

func toolAttr(a *syscall.SysProcAttr, fd int) *syscall.SysProcAttr {
	a.Pdeathsig = syscall.SIGKILL
	if fd >= 0 {
		a.UseCgroupFD, a.CgroupFD = true, fd
	}
	return a
}
