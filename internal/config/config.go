// Package config loads figaro's configuration.
//
// Layout:
//
//	~/.config/figaro/
//	  config.toml              # top-level: default provider, log
//	  providers/
//	    anthropic/
//	      config.toml          # provider-specific settings
//	      auth.json            # hush-encrypted credentials
//	    openai/
//	      config.toml
//	      auth.json
//
// The top-level config.toml names the default provider. Each provider
// directory contains its own config and auth. Main discovers all
// configured providers by scanning the providers directory.
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

	// DefaultModel overrides the provider's default model (optional).
	DefaultModel string `toml:"default_model"`

	// EchoPrompt controls whether the CLI prints the user's prompt
	// before the response streams. Pointer so we can distinguish
	// "unset → default true" from "explicit false". The setting
	// lives only on the CLI side; it never enters the conversation.
	EchoPrompt *bool `toml:"echo_prompt"`

	// Log configures output destinations.
	Log LogConfig `toml:"log"`
}

// EchoPrompt returns whether the CLI should echo the user's prompt
// back before streaming the response. Defaults to true.
func (l *Loaded) EchoPrompt() bool {
	if l.Config.EchoPrompt == nil {
		return true
	}
	return *l.Config.EchoPrompt
}

// LogConfig controls where structured output goes.
type LogConfig struct {
	RPCFile string `toml:"rpc_file"`
}

// AnthropicProvider is the concrete config for an anthropic provider directory.
type AnthropicProvider struct {
	Model     string `toml:"model"`
	MaxTokens int    `toml:"max_tokens"`
	APIKey    string `toml:"api_key"`

	// ReminderRenderer selects how chalkboard reminders are surfaced
	// to the model. "tag" (default) wraps each rendered body in
	// <system-reminder name="…">…</system-reminder> blocks attached to
	// the latest user message. "tool" emits a synthetic assistant
	// tool_use + user tool_result pair after the latest user message.
	// The synthetic tool is NOT declared in the request's tools list,
	// so the model cannot call it going forward — it reads as
	// "I called something, got something back, moving on."
	ReminderRenderer string `toml:"reminder_renderer"`
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

// ListProviders returns the names of all configured providers
// by scanning the providers directory.
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

// Log returns the log config with paths expanded.
func (l *Loaded) Log() LogConfig {
	log := l.Config.Log
	home, _ := os.UserHomeDir()
	log.RPCFile = expandHome(log.RPCFile, home)
	return log
}

// DefaultConfigDir returns the config directory, respecting XDG.
func DefaultConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "figaro")
}

// Load reads the top-level config. Returns defaults if the file
// doesn't exist.
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
	home, _ := os.UserHomeDir()
	return Config{
		DefaultProvider: "anthropic",
		Log: LogConfig{
			RPCFile: filepath.Join(home, ".local", "state", "figaro", "rpc.jsonl"),
		},
	}
}

func expandHome(path, home string) string {
	if len(path) > 1 && path[:2] == "~/" {
		return filepath.Join(home, path[2:])
	}
	return path
}
