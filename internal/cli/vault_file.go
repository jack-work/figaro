package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/jack-work/hush/managed"
)

// File unlock: the passphrase is a random string in a 0600 file next to the
// identity, for hosts with no keyring and no terminal (WSL cannot create a
// Secret Service collection without a prompter). The security claim is exactly
// the file's mode.
const passphraseFileName = "passphrase"

// setupFileUnlock returns the live passphrase bytes and updates the in-memory
// config too: hush loaded it before this file existed.
func setupFileUnlock(h *managed.Hush) ([]byte, string, error) {
	cfg := h.Config()
	if cfg == nil {
		return nil, "", fmt.Errorf("vault: no hush config resolved")
	}
	dir := cfg.ConfigDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("vault: create %s: %w", dir, err)
	}
	path, err := filepath.Abs(filepath.Join(dir, passphraseFileName))
	if err != nil {
		return nil, "", err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("vault: read random bytes: %w", err)
	}
	pp := []byte(base64.StdEncoding.EncodeToString(raw))
	for i := range raw {
		raw[i] = 0
	}

	if err := writePrivateFile(path, append(append([]byte(nil), pp...), '\n')); err != nil {
		return nil, "", err
	}
	if err := writeHushUnlockFile(filepath.Join(dir, "hush.toml"), path); err != nil {
		return nil, "", err
	}
	cfg.Unlock.Method = "file"
	cfg.Unlock.File = path
	return pp, path, nil
}

// The temp file is born 0600 in the destination directory, so the rename never
// crosses a filesystem and the passphrase is never readable by anyone else.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("vault: create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("vault: write %s: %w", tmp, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("vault: chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("vault: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("vault: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("vault: rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

// writeHushUnlockFile rewrites only [unlock]: a blind overwrite would reset ttl
// to hush's 30m default.
func writeHushUnlockFile(configPath, passphrasePath string) error {
	doc := map[string]any{}
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vault: read %s: %w", configPath, err)
	}
	if err == nil {
		if err := toml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("vault: parse %s: %w", configPath, err)
		}
	}
	unlock, _ := doc["unlock"].(map[string]any)
	if unlock == nil {
		unlock = map[string]any{}
	}
	unlock["method"] = "file"
	unlock["file"] = passphrasePath
	doc["unlock"] = unlock

	var buf bytes.Buffer
	fmt.Fprintln(&buf, "# Written by `figaro vault init --file`.")
	fmt.Fprintln(&buf, "# The passphrase lives in the file named below, mode 0600.")
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return fmt.Errorf("vault: encode %s: %w", configPath, err)
	}
	if err := writePrivateFile(configPath, buf.Bytes()); err != nil {
		return err
	}
	return nil
}
