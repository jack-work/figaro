// Package config loads figaro's configuration from ~/.config/figaro/.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level figaro configuration.
type Config struct {
	// DefaultProvider is the provider used when -p is not specified.
	DefaultProvider string `toml:"default_provider"`

	// DefaultModel overrides the provider's default model.
	DefaultModel string `toml:"default_model"`

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

// AnthropicProvider is the config for an anthropic provider.
type AnthropicProvider struct {
	Model     string `toml:"model"`
	MaxTokens int    `toml:"max_tokens"`
	APIKey    string `toml:"api_key"`

	// ReminderRenderer selects how chalkboard reminders are projected.
	// "tag" (default) uses <system-reminder> blocks. "tool" uses
	// synthetic tool_use/tool_result pairs.
	ReminderRenderer string `toml:"reminder_renderer"`

	// UseOfficialSDK routes Anthropic traffic through the official
	// anthropic-sdk-go-backed provider (internal/provider/anthropicsdk).
	// The legacy in-tree implementation stays the default.
	UseOfficialSDK bool `toml:"use_official_sdk"`
}

// Loaded holds the parsed top-level config plus path context.
type Loaded struct {
	Config     Config
	ConfigDir  string // e.g. ~/.config/figaro
	ConfigPath string // e.g. ~/.config/figaro/config.toml
}

// ProviderDir returns the path to a named provider's directory.
func (l *Loaded) ProviderDir(name string) string {
	return filepath.Join(l.ConfigDir, "providers", name)
}

// ProviderConfigPath returns the path to a provider's config.toml.
func (l *Loaded) ProviderConfigPath(name string) string {
	return filepath.Join(l.ProviderDir(name), "config.toml")
}

// ProviderAuthPath returns the path to a provider's auth.toml.
func (l *Loaded) ProviderAuthPath(name string) string {
	return filepath.Join(l.ProviderDir(name), "auth.toml")
}

// ListProviders returns all configured provider names.
func (l *Loaded) ListProviders() []string {
	dir := filepath.Join(l.ConfigDir, "providers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// LoadProviderConfig decodes a provider's config.toml into target.
func (l *Loaded) LoadProviderConfig(name string, target interface{}) error {
	path := l.ProviderConfigPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no config file means use defaults
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
	return Config{
		DefaultProvider: "anthropic",
	}
}
