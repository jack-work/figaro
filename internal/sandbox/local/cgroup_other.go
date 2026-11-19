//go:build !linux

package local

import "syscall"

func toolAttr(a *syscall.SysProcAttr, _ int) *syscall.SysProcAttr { return a }
