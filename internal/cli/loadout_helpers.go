// Package cli — helpers shared between provider construction and
// the first-run / loadout flows.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

// readLoadoutKnobs loads a named loadout via the outfitter and
// extracts the provider.Knobs from system.* keys. Returns an empty
// Knobs on any failure (callers are expected to substitute defaults).
func readLoadoutKnobs(loaded *config.Loaded, loadoutName string) providerPkg.Knobs {
	if loaded == nil || loadoutName == "" {
		return providerPkg.Knobs{}
	}
	ofit := outfit.New(loaded.ConfigDir)
	patch, err := ofit.Load(loadoutName)
	if err != nil || patch.IsEmpty() {
		return providerPkg.Knobs{}
	}
	pickStr := func(key string) string {
		raw, ok := patch.Set[key]
		if !ok {
			return ""
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	pickInt := func(key string) int {
		raw, ok := patch.Set[key]
		if !ok {
			return 0
		}
		var n int
		_ = json.Unmarshal(raw, &n)
		return n
	}
	pickBool := func(key string) bool {
		raw, ok := patch.Set[key]
		if !ok {
			return false
		}
		var b bool
		_ = json.Unmarshal(raw, &b)
		return b
	}
	return providerPkg.Knobs{
		Model:            pickStr("system.model"),
		MaxTokens:        pickInt("system.max_tokens"),
		ReminderRenderer: pickStr("system.reminder_renderer"),
		UseOfficialSDK:   pickBool("system.use_official_sdk"),
	}
}

// encryptedAPIKey reads an AGE-encrypted api_key from a provider auth
// TOML file and decrypts it through hush. Mtime-cached.
//
// Plaintext api_key values are handled by the upstream auth.ConfigValue
// strategy; this one only fires when the on-disk value carries the
// "AGE-ENC[" prefix.
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

func (e *encryptedAPIKey) Invalidate(token string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached == token {
		e.cached = ""
		e.cachedAt = time.Time{}
	}
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
