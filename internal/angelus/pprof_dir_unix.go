//go:build !windows

package angelus

import (
	"os"
	"syscall"
)

func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
