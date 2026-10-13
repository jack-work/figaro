//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// Own process group holding the terminal, so the tty's Ctrl-C raises SIGINT in
// the editor, not figaro. Ctty is a child descriptor: fd 0 is the terminal.
func editorProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
}

// SIGTTOU is what the kernel raises when a background process touches the
// foreground group, and its default action stops the process.
func reclaimTerminal() {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	_ = unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, unix.Getpgrp())
}
