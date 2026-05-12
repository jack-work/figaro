package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/internal/message"
)

// FileBackend is a directory-backed Backend. One subdirectory per aria.
type FileBackend struct {
	dir string
}

var _ Backend = (*FileBackend)(nil)

const (
	ariaFile = "aria.jsonl"   // legacy: NDJSON file per aria
	ariaDir  = "aria"         // figwal: dir of segments per aria
	metaFile = "meta.json"

	// envUseFigwal, when set to a truthy value, makes new arias and
	// new translator caches default to the figwal-backed Stream. The
	// on-disk evidence (a file vs a dir) still takes precedence for
	// existing arias.
	envUseFigwal = "FIGARO_USE_FIGWAL"
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

func (b *FileBackend) Open(ariaID string) (Log[message.Message], error) {
	if ariaID == "" {
		return nil, fmt.Errorf("file backend: empty aria id")
	}
	ariaRoot := filepath.Join(b.dir, ariaID)
	walDir := filepath.Join(ariaRoot, ariaDir)
	jsonlPath := filepath.Join(ariaRoot, ariaFile)
	if useFigwal := pickStreamFormat(walDir, jsonlPath); useFigwal {
		return OpenFigwalLog[message.Message](walDir)
	}
	return OpenFileLog[message.Message](jsonlPath)
}

func (b *FileBackend) OpenTranslation(ariaID, providerName string) (Log[[]json.RawMessage], error) {
	if ariaID == "" || providerName == "" {
		return nil, fmt.Errorf("file backend: empty aria id or provider name")
	}
	tDir := filepath.Join(b.dir, ariaID, "translations")
	walDir := filepath.Join(tDir, providerName)
	jsonlPath := filepath.Join(tDir, providerName+".jsonl")
	if useFigwal := pickStreamFormat(walDir, jsonlPath); useFigwal {
		return OpenFigwalLog[[]json.RawMessage](walDir)
	}
	return OpenFileLog[[]json.RawMessage](jsonlPath)
}

// pickStreamFormat returns true when the figwal-backed format should
// be used. On-disk evidence wins: if the figwal dir already exists, we
// must use figwal; if the legacy file already exists, we must use the
// legacy log. For a brand-new log, the FIGARO_USE_FIGWAL env var
// selects the default.
func pickStreamFormat(walDir, legacyFile string) bool {
	if info, err := os.Stat(walDir); err == nil && info.IsDir() {
		return true
	}
	if _, err := os.Stat(legacyFile); err == nil {
		return false
	}
	switch os.Getenv(envUseFigwal) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
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

func (b *FileBackend) TranslationMeta(ariaID, providerName string) (*TranslationMeta, error) {
	path := filepath.Join(b.dir, ariaID, "translations", providerName+".meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("file backend: read translation meta: %w", err)
	}
	var m TranslationMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("file backend: parse translation meta: %w", err)
	}
	return &m, nil
}

func (b *FileBackend) SetTranslationMeta(ariaID, providerName string, meta *TranslationMeta) error {
	if meta == nil {
		return nil
	}
	dir := filepath.Join(b.dir, ariaID, "translations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("file backend: create translations dir: %w", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("file backend: marshal translation meta: %w", err)
	}
	return writeAtomic(filepath.Join(dir, providerName+".meta.json"), data)
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

// writeAtomic writes via tmp+rename.
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
