package store

import (
	"os"

	"golang.org/x/sys/unix"
)

func cloneFile(out, in *os.File) error {
	return unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
}
