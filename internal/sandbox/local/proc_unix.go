//go:build !windows

package local

import (
	"os"
	"os/exec"
	"syscall"
)

func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func afterStart(_ *exec.Cmd) {}

func afterWait(_ *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func signalTree(cmd *exec.Cmd, sig syscall.Signal) error {
	return syscall.Kill(-cmd.Process.Pid, sig)
}

func interruptible(f *os.File) *os.File {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return f
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return f
	}
	f.Close()
	return os.NewFile(uintptr(fd), f.Name())
}
