//go:build !unix

package xwal

import (
	"os"
	"path/filepath"
)

const lockName = ".lock"

// Non-unix builds get no advisory lock; single-writer is not enforced.
func lockRoot(root string) (*os.File, error) {
	return os.OpenFile(filepath.Join(root, lockName), os.O_CREATE|os.O_RDWR, 0o644)
}

func unlockRoot(f *os.File) error {
	if f == nil {
		return nil
	}
	return f.Close()
}
