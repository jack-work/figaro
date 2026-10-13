package local

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

type FS struct{}

var (
	locksMu sync.Mutex
	locks   = map[string]*sync.Mutex{}
)

func (FS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (FS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (FS) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (FS) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }

func (FS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (FS) Update(path string, fn func(old []byte) ([]byte, error)) error {
	key := filepath.Clean(path)
	locksMu.Lock()
	mu, ok := locks[key]
	if !ok {
		mu = &sync.Mutex{}
		locks[key] = mu
	}
	locksMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data, err := fn(old)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
