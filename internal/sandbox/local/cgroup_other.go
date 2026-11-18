//go:build !linux

package local

import "syscall"

func intoCgroup(a *syscall.SysProcAttr, _ int) *syscall.SysProcAttr { return a }
