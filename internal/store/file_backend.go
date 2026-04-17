package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileBackend is the directory-backed Backend implementation. Each
// aria is one JSON file named <ariaID>.json inside the backend's
// root directory. Atomic write-to-tmp + rename on every Checkpoint.
//
// This is the default backend and has zero external dependencies.
type FileBackend struct {
	dir string
}

var _ Backend = (*FileBackend)(nil)

// NewFileBackend opens (or creates) a directory-backed aria store.
// The directory is created with 0700 if missing.
func NewFileBackend(dir string) (*FileBackend, error) {
	if dir == "" {
		return nil, fmt.Errorf("file backend: empty directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("file backend: create %s: %w", dir, err)
	}
	return &FileBackend{dir: dir}, nil
}

// Dir returns the backing directory path. Useful for tests and logs.
func (b *FileBackend) Dir() string {
	return b.dir
}

// Open returns a FileStore for the given aria, creating the file
// lazily on first Checkpoint if it does not yet exist.
func (b *FileBackend) Open(ariaID string) (Downstream, error) {
	if ariaID == "" {
		return nil, fmt.Errorf("file backend: empty aria id")
	}
	path := filepath.Join(b.dir, ariaID+".json")
	return NewFileStore(path)
}

// List scans the backing directory and returns metadata for every
// aria file. Non-JSON entries are ignored; unparseable entries are
// skipped silently (a corrupt file should not block startup).
func (b *FileBackend) List() ([]AriaInfo, error) {
	return listAriasInDir(b.dir)
}

// Remove deletes the aria's JSON file. A missing file is not an error.
func (b *FileBackend) Remove(ariaID string) error {
	path := filepath.Join(b.dir, ariaID+".json")
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Close is a no-op for the file backend — each FileStore holds its
// own mutex and its own file descriptor is opened transiently per
// write.
func (b *FileBackend) Close() error {
	return nil
}
