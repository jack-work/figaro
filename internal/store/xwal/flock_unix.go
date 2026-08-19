//go:build unix

package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const lockName = ".lock"

func lockRoot(root string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(root, lockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("xwal: store %s already has a writer (flock: %v)", root, err)
	}
	return f, nil
}

func unlockRoot(f *os.File) error {
	if f == nil {
		return nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
