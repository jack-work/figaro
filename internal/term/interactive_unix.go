//go:build !windows

package term

import (
	"os"

	"golang.org/x/sys/unix"
)

func Interactive(in, out *os.File) bool {
	if !IsTerminal(int(in.Fd())) || !IsTerminal(int(out.Fd())) {
		return false
	}
	fg, err := unix.IoctlGetInt(int(in.Fd()), unix.TIOCGPGRP)
	return err == nil && fg == unix.Getpgrp()
}
