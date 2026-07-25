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

	// Interactive controls whether the first-run wizard uses a rich
	// bubbletea/huh-driven TUI. Default true. When false, falls back
	// to plain numbered prompts (the pre-TUI behavior). Useful for
	// CI / scripted invocations that prefer not to deal with raw mode.
	Interactive *bool `toml:"interactive"`

	// StreamCPS is the pacer's target chars/sec. 0 disables pacing.
	// Pointer to distinguish unset (default) from explicit 0.
	StreamCPS *int `toml:"stream_cps"`

	// StreamFirstByteBypassMs is the sync-write window for TTFT.
	// Default 80.
	StreamFirstByteBypassMs *int `toml:"stream_first_byte_bypass_ms"`

	// CheckUpdates controls the passive one-liner nudge on startup
	// when a newer figaro release is available on the module proxy.
	// Pointer to distinguish unset (default true) from explicit false.
	// The explicit `figaro update` command is *always* available;
	// this only toggles the automatic background hint.
	CheckUpdates *bool `toml:"check_updates"`

	// UpdateCheckTTLHours bounds how often the proxy is asked. Default
	// 24. Zero disables caching (each check hits the network) — useful
	// only for testing.
	UpdateCheckTTLHours *int `toml:"update_check_ttl_hours"`

	// RefSigil is the prefix character for chalkboard references in
	// prompts and tab completion. Must be "@" or ":". Default "@".
	RefSigil string `toml:"ref_sigil"`

	// Wire bounds paginated reads. See WireConfig.
	Wire WireConfig `toml:"wire"`
}

// WireConfig bounds a paginated read. The budget is spent in BYTES and
// paid out in whole NODES — a page never splits a node, and always
// carries at least one even when that node alone exceeds the budget.
// Node granularity is only safe because tool output is already clamped
// (compose.composeBashCap); the full text stays in the canonical IR.
type WireConfig struct {
	// PageBudget is the server's default page size in bytes, used when a
	// client names no budget of its own. Default 65536.
	PageBudget *int `toml:"page_budget"`

	// PageBudgetMax is the ceiling clamped onto a client's request, so a
	// client can never make the server materialize an unbounded page.
	// Default 524288.
	PageBudgetMax *int `toml:"page_budget_max"`
}

const (
	defaultPageBudget    = 65536
	defaultPageBudgetMax = 524288
)

// PageBudget returns the server-side default page budget in bytes.
func (l *Loaded) PageBudget() int {
	if l.Config.Wire.PageBudget == nil {
		return defaultPageBudget
	}
	return *l.Config.Wire.PageBudget
}

// PageBudgetMax returns the ceiling on a client-requested budget.
func (l *Loaded) PageBudgetMax() int {
	if l.Config.Wire.PageBudgetMax == nil {
		return defaultPageBudgetMax
	}
	return *l.Config.Wire.PageBudgetMax
}

// ClampPageBudget resolves a client's requested budget against policy:
// a non-positive request means "use the server default", and any request
// is capped at the maximum. Never trust the client to bound server work.
func (l *Loaded) ClampPageBudget(requested int) int {
	if requested <= 0 {
		requested = l.PageBudget()
	}
	if max := l.PageBudgetMax(); requested > max {
		return max
	}
	return requested
}

// validateWire rejects budgets that cannot describe a page.
func (c Config) validateWire() error {
	if b := c.Wire.PageBudget; b != nil && *b <= 0 {
		return fmt.Errorf("config: wire.page_budget must be > 0, got %d", *b)
	}
	if m := c.Wire.PageBudgetMax; m != nil {
		if *m <= 0 {
			return fmt.Errorf("config: wire.page_budget_max must be > 0, got %d", *m)
		}
		if b := c.Wire.PageBudget; b != nil && *m < *b {
			return fmt.Errorf("config: wire.page_budget_max (%d) must be >= wire.page_budget (%d)", *m, *b)
		}
	}
	return nil
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

// Interactive returns whether the first-run wizard should use a rich
// TUI. Default true.
func (l *Loaded) Interactive() bool {
	if l.Config.Interactive == nil {
		return true
	}
	return *l.Config.Interactive
}

// RefSigil returns the chalkboard reference sigil. Default "@".
// Returns an error if the configured value is not "@" or ":".
func (l *Loaded) RefSigil() (string, error) {
	s := l.Config.RefSigil
	if s == "" {
		return "@", nil
	}
	if s == "@" || s == ":" {
		return s, nil
	}
	return "", fmt.Errorf("config: ref_sigil must be \"@\" or \":\", got %q", s)
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

// CheckUpdates returns whether to run the passive startup update check.
// Default true. Users who prefer silence can set `check_updates = false`
// in ~/.config/figaro/config.toml.
func (l *Loaded) CheckUpdates() bool {
	if l.Config.CheckUpdates == nil {
		return true
	}
	return *l.Config.CheckUpdates
}

// UpdateCheckTTLHours returns the update-check cache TTL. Default 24h.
func (l *Loaded) UpdateCheckTTLHours() int {
	if l.Config.UpdateCheckTTLHours == nil {
		return 24
	}
	return *l.Config.UpdateCheckTTLHours
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
	// FIGARO_CONFIG_DIR is an explicit override used as-is (no
	// "figaro" suffix appended) — lets dev shells point at an
	// isolated config tree without touching the user's real one.
	if d := os.Getenv("FIGARO_CONFIG_DIR"); d != "" {
		return d
	}
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

	if err := cfg.validateWire(); err != nil {
		return nil, err
	}

	return &Loaded{Config: cfg, ConfigDir: configDir, ConfigPath: configPath}, nil
}

func defaultConfig() Config {
	// No DefaultLoadout: empty triggers the first-run flow.
	return Config{}
}
