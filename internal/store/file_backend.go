package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileBackend is the directory-backed Backend implementation. Each
// aria is its own subdirectory under the backend's root, containing
// aria.jsonl (NDJSON of message.Message values) and meta.json
// (transitional AriaMeta).
//
// Layout:
//
//	{root}/
//	├── {ariaID-1}/
//	│   ├── aria.jsonl
//	│   └── meta.json
//	├── {ariaID-2}/
//	│   ├── aria.jsonl
//	│   └── meta.json
//	└── ...
//
// This is the default backend and has zero external dependencies.
type FileBackend struct {
	dir string
}

var _ Backend = (*FileBackend)(nil)

// NewFileBackend opens (or creates) a directory-backed aria store.
// The root directory is created with 0700 if missing.
func NewFileBackend(dir string) (*FileBackend, error) {
	if dir == "" {
		return nil, fmt.Errorf("file backend: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("file backend: create %s: %w", dir, err)
	}
	return &FileBackend{dir: dir}, nil
}

// Dir returns the root backing directory path.
func (b *FileBackend) Dir() string {
	return b.dir
}

// Open returns a FileStore for the given aria. The aria's directory
// (root/{ariaID}/) is created lazily on first write; reading from a
// non-existent aria returns an empty store.
func (b *FileBackend) Open(ariaID string) (Downstream, error) {
	if ariaID == "" {
		return nil, fmt.Errorf("file backend: empty aria id")
	}
	ariaDir := filepath.Join(b.dir, ariaID)
	return NewFileStore(ariaDir)
}

// List scans the backing directory and returns metadata for every
// aria subdirectory containing aria.jsonl. Subdirectories without
// aria.jsonl are ignored; unparseable entries are skipped silently
// (a corrupt aria should not block startup).
func (b *FileBackend) List() ([]AriaInfo, error) {
	return listAriasInDir(b.dir)
}

// Remove deletes the aria's entire directory. Missing directory is not
// an error.
func (b *FileBackend) Remove(ariaID string) error {
	ariaDir := filepath.Join(b.dir, ariaID)
	err := os.RemoveAll(ariaDir)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Close is a no-op for the file backend — each FileStore manages its
// own descriptors transiently per write.
func (b *FileBackend) Close() error {
	return nil
}
