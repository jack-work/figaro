package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/internal/message"
)

// FileBackend is the directory-backed Backend. Each aria is its own
// subdirectory containing the figaro IR stream + per-provider
// translation streams + meta.
//
// Layout:
//
//	{root}/
//	├── {ariaID-1}/
//	│   ├── aria.jsonl                NDJSON of Entry[Message] (figaro IR stream)
//	│   ├── meta.json                 AriaMeta (transitional)
//	│   └── translations/
//	│       └── {provider}.jsonl      NDJSON of Entry[[]RawMessage]
//	├── {ariaID-2}/...
//
// Zero external dependencies.
type FileBackend struct {
	dir string
}

var _ Backend = (*FileBackend)(nil)

const (
	ariaFile = "aria.jsonl"
	metaFile = "meta.json"
)

// NewFileBackend opens (or creates) a directory-backed aria store.
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
func (b *FileBackend) Dir() string { return b.dir }

func (b *FileBackend) Open(ariaID string) (Stream[message.Message], error) {
	if ariaID == "" {
		return nil, fmt.Errorf("file backend: empty aria id")
	}
	return OpenFileStream[message.Message](filepath.Join(b.dir, ariaID, ariaFile))
}

func (b *FileBackend) OpenTranslation(ariaID, providerName string) (Stream[[]json.RawMessage], error) {
	if ariaID == "" || providerName == "" {
		return nil, fmt.Errorf("file backend: empty aria id or provider name")
	}
	path := filepath.Join(b.dir, ariaID, "translations", providerName+".jsonl")
	return OpenFileStream[[]json.RawMessage](path)
}

func (b *FileBackend) Meta(ariaID string) (*AriaMeta, error) {
	data, err := os.ReadFile(filepath.Join(b.dir, ariaID, metaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("file backend: read meta: %w", err)
	}
	var m AriaMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("file backend: parse meta: %w", err)
	}
	return &m, nil
}

func (b *FileBackend) SetMeta(ariaID string, meta *AriaMeta) error {
	if meta == nil {
		return nil
	}
	ariaDir := filepath.Join(b.dir, ariaID)
	if err := os.MkdirAll(ariaDir, 0o700); err != nil {
		return fmt.Errorf("file backend: create aria dir: %w", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("file backend: marshal meta: %w", err)
	}
	return writeAtomic(filepath.Join(ariaDir, metaFile), data)
}

func (b *FileBackend) List() ([]AriaInfo, error) {
	return listAriasInDir(b.dir)
}

func (b *FileBackend) Remove(ariaID string) error {
	err := os.RemoveAll(filepath.Join(b.dir, ariaID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (b *FileBackend) Close() error { return nil }

// writeAtomic writes data to path via the rewrite-tmp-rename pattern.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
