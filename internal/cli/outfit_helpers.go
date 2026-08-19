// Package cli: helpers shared between provider construction and
// the first-run / outfit flows.
package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

// encryptedAPIKey reads an AGE-encrypted api_key from a provider auth
// TOML file and decrypts it through hush. Mtime-cached.
type encryptedAPIKey struct {
	Hush       *hush.Client
	ConfigPath string

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

var _ auth.CredentialStrategy = (*encryptedAPIKey)(nil)

func (e *encryptedAPIKey) TryResolve() (string, bool, error) {
	if e.Hush == nil || e.ConfigPath == "" {
		return "", false, nil
	}
	info, err := os.Stat(e.ConfigPath)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", e.ConfigPath, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached != "" && info.ModTime().Equal(e.cachedAt) {
		return e.cached, true, nil
	}
	var pa config.ProviderAuth
	if err := loadProviderAuthFrom(e.ConfigPath, &pa); err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
		return "", false, nil
	}
	res, err := e.Hush.Decrypt(map[string]string{"v": pa.APIKey})
	if err != nil {
		return "", false, fmt.Errorf("hush decrypt %s: %w", e.ConfigPath, err)
	}
	plain := res["v"]
	e.cached = plain
	e.cachedAt = info.ModTime()
	return plain, true, nil
}

func (e *encryptedAPIKey) Invalidate(token string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached == token {
		e.cached = ""
		e.cachedAt = time.Time{}
	}
	return nil
}

// loadProviderAuthFrom decodes a provider auth TOML file. Thin
// wrapper that bypasses the config.Loaded path so encryptedAPIKey
// can target a precomputed path.
func loadProviderAuthFrom(path string, target *config.ProviderAuth) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return toml.Unmarshal(data, target)
}
