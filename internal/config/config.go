// Package config loads figaro's configuration from ~/.config/figaro/.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level figaro configuration. Provider/model knobs
// have moved into loadouts; this file holds only the chosen loadout
// and CLI-side ergonomics.
type Config struct {
	// DefaultLoadout names the loadout used when -L is not specified.
	// Empty triggers the first-run flow (see rpc.ErrNoDefaultLoadout).
	DefaultLoadout string `toml:"default_loadout"`

	// EchoPrompt controls whether the CLI echoes the prompt.
	// Pointer to distinguish unset (default true) from explicit false.
	EchoPrompt *bool `toml:"echo_prompt"`

	// StatusLine controls the status banner. Default true.
	StatusLine *bool `toml:"status_line"`

	// StreamCPS is the pacer's target chars/sec. 0 disables pacing.
	// Pointer to distinguish unset (default) from explicit 0.
	StreamCPS *int `toml:"stream_cps"`

	// StreamFirstByteBypassMs is the sync-write window for TTFT.
	// Default 80.
	StreamFirstByteBypassMs *int `toml:"stream_first_byte_bypass_ms"`
}

// EchoPrompt returns whether to echo the prompt. Default true.
func (l *Loaded) EchoPrompt() bool {
	if l.Config.EchoPrompt == nil {
		return true
	}
	return *l.Config.EchoPrompt
}

// StatusLine returns whether to show status banners. Default true.
func (l *Loaded) StatusLine() bool {
	if l.Config.StatusLine == nil {
		return true
	}
	return *l.Config.StatusLine
}

// StreamCPS returns the pacer rate. Default 200.
func (l *Loaded) StreamCPS() int {
	if l.Config.StreamCPS == nil {
		return 200
	}
	return *l.Config.StreamCPS
}

// StreamFirstByteBypassMs returns the TTFT bypass window. Default 80ms.
func (l *Loaded) StreamFirstByteBypassMs() int {
	if l.Config.StreamFirstByteBypassMs == nil {
		return 80
	}
	return *l.Config.StreamFirstByteBypassMs
}

// ProviderAuth holds credentials for one provider. The on-disk file
// lives at providers/<name>.toml (flat — no per-provider subdirectory).
// Secret fields are AGE-encrypted at rest; callers must decrypt
// through hush before use.
type ProviderAuth struct {
	// APIKey is an opaque static credential. AGE-ENC[...] when
	// encrypted; plain string otherwise.
	APIKey string `toml:"api_key"`

	// OAuth tokens (AGE-encrypted when present).
	AccessToken  string `toml:"access_token"`
	RefreshToken string `toml:"refresh_token"`
	ExpiresAt    int64  `toml:"expires_at"`
}

// Loaded holds the parsed top-level config plus path context.
type Loaded struct {
	Config     Config
	ConfigDir  string // e.g. ~/.config/figaro
	ConfigPath string // e.g. ~/.config/figaro/config.toml
}

// ProviderAuthPath returns the path to a provider's auth file
// (providers/<name>.toml — flat, no subdirectory).
func (l *Loaded) ProviderAuthPath(name string) string {
	return filepath.Join(l.ConfigDir, "providers", name+".toml")
}

// LoadoutsDir returns the directory housing loadout TOML files.
func (l *Loaded) LoadoutsDir() string {
	return filepath.Join(l.ConfigDir, "loadouts")
}

// LoadoutPath returns the path to a named loadout file.
func (l *Loaded) LoadoutPath(name string) string {
	return filepath.Join(l.LoadoutsDir(), name+".toml")
}

// ListProviders returns provider names with auth files on disk.
func (l *Loaded) ListProviders() []string {
	dir := filepath.Join(l.ConfigDir, "providers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".toml"))
	}
	return names
}

// ListLoadouts returns the names of every loadout file on disk.
func (l *Loaded) ListLoadouts() []string {
	entries, err := os.ReadDir(l.LoadoutsDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".toml"))
	}
	return names
}

// LoadProviderAuth decodes a provider's auth.toml into target.
// Returns nil with no error when the file is absent (lets callers
// fall back to other strategies).
func (l *Loaded) LoadProviderAuth(name string, target interface{}) error {
	path := l.ProviderAuthPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// DefaultConfigDir returns the config directory (XDG-aware).
func DefaultConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "figaro")
}

// Load reads the top-level config. Returns defaults if missing.
func Load(configDir string) (*Loaded, error) {
	configPath := filepath.Join(configDir, "config.toml")
	cfg := defaultConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Loaded{Config: cfg, ConfigDir: configDir, ConfigPath: configPath}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", configPath, err)
	}

	return &Loaded{Config: cfg, ConfigDir: configDir, ConfigPath: configPath}, nil
}

func defaultConfig() Config {
	// No DefaultLoadout: empty triggers the first-run flow.
	return Config{}
}
