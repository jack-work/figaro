//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"syscall"
)

func ownManagerSocket(socket string) bool {
	return trustedManagerDir(filepath.Dir(socket), os.Getuid()) && socketExists(socket)
}

func trustedManagerDir(dir string, uid int) bool {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
