//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// editorProcAttr puts the editor in its own process group AND gives that group
// the terminal, so the tty's own Ctrl-C raises SIGINT in the editor and not in
// figaro. Ctty is a descriptor number in the CHILD, where fd 0 is the terminal
// we passed it.
func editorProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
}

// reclaimTerminal takes the foreground group back after the editor is gone.
// SIGTTOU is ignored across the call because that is exactly what the kernel
// raises when a background process touches the terminal's foreground group,
// and its default action stops the process.
func reclaimTerminal() {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	_ = unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, unix.Getpgrp())
}
