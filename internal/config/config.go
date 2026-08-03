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

	// Trunks enables the trunk capability: a presentation hierarchy an
	// aria can be promoted within, independent of where its history comes
	// from, and a delete that follows it. Default true.
	//
	// With it off, figaro has no trunk pstate at all: the hierarchy IS the
	// fork topology, `fig promote` reports that the server cannot serve it,
	// and a delete can never orphan a survivor because the two agree.
	Trunks *bool `toml:"trunks"`

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

	// StreamEmitIntervalMs coalesces live streaming emits (recompose +
	// broadcast). Default 90 (~11fps). A structural change always emits
	// immediately, so this only trades frame smoothness against CPU and
	// bytes-on-the-wire: raise it on a slow link, drop it to 0 to emit on
	// every chunk. Correctness does not depend on it.
	StreamEmitIntervalMs *int `toml:"stream_emit_interval_ms"`

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

	// Store bounds the on-disk WAL geometry. See StoreConfig.
	Store StoreConfig `toml:"store"`

	// Authz gates the RPC surface. See AuthzConfig.
	Authz AuthzConfig `toml:"authz"`
}

// AuthzConfig selects the authentication provider and the authorization
// policy for the RPC surface (see internal/authz).
//
// Both default OFF, so a config that says nothing behaves exactly as figaro
// did before authorization existed. Turning them on is a deliberate act,
// which is the whole point: a credential nobody can disable is not a
// credential, and a policy nobody can select is not a policy.
type AuthzConfig struct {
	// CallerIdentity enables the authn provider that reads the caller's aria
	// id from the x-internal-figaro-id params field (rpc.CallerKey). When
	// false, every request is anonymous no matter what it presented — the
	// server has chosen not to trust the wire.
	//
	// Pointer so unset is distinguishable from an explicit false.
	CallerIdentity *bool `toml:"caller_identity"`

	// Policy names the authorization policy. "" or "allow-all" gates
	// nothing; "default" selects authz.DefaultRules (today: refuse a
	// self-fork issued from inside a running turn).
	//
	// A name rather than a bool because the policy is a seam, not a switch:
	// the next policy is a different table, not a second flag.
	Policy string `toml:"policy"`
}

// StoreConfig sizes the aria log on disk.
type StoreConfig struct {
	// SegmentSize bounds ONE WAL segment file in bytes; a segment rolls when
	// the next record would not fit. Default 2 MiB (~1300 IR entries at the
	// measured 1.6KB/entry). Raise it to roll less often on a big store;
	// lower it to keep individual files small. New segments only — existing
	// ones keep their size and simply stop growing.
	SegmentSize *int `toml:"segment_size"`
}

const (
	defaultSegmentSize = 2 * 1024 * 1024
	// minSegmentSize is a HARD floor, not taste: figwal fails an append with
	// "payload too large for segment size" when a single record cannot fit
	// inside one empty segment.
	minSegmentSize = 1024 * 1024
)

// SegmentSize returns the WAL segment size in bytes. Nil-safe, so a store
// opened without config still gets the same geometry as one opened with it.
func (l *Loaded) SegmentSize() int {
	if l == nil || l.Config.Store.SegmentSize == nil {
		return defaultSegmentSize
	}
	return *l.Config.Store.SegmentSize
}

// imageShareNum/imageShareDen is the fraction of a WAL segment that inlined
// imagery may claim: two thirds. The remaining third holds the tool_result
// TEXT sharing the record (bounded at MaxOutputBytes per tool) plus the JSON
// envelope, and figwal needs the whole record to fit an EMPTY segment with
// nothing to spare.
const (
	imageShareNum = 2
	imageShareDen = 3
)

// InlineImageBudget is the SINGLE policy point for how many base64 bytes of
// tool imagery one tool_result record may carry.
//
// It is derived, not chosen. The tic is ONE figwal record and a record larger
// than a segment fails the append outright, taking the turn with it — so the
// ceiling has to move with `store.segment_size` rather than being a constant
// pinned to the smallest legal configuration. A user who raises the segment
// size to hold bigger screenshots gets bigger screenshots; one who lowers it
// to the floor gets a proportionally tighter budget instead of a broken store.
//
// The provider ceiling caps the top end: past ~5MB per image the APIs refuse
// it anyway, so a 64MiB segment buys context cost, not capability.
func (l *Loaded) InlineImageBudget() int {
	budget := l.SegmentSize() / imageShareDen * imageShareNum
	if budget > maxInlineImageBytes {
		budget = maxInlineImageBytes
	}
	return budget
}

// maxInlineImageBytes mirrors tool.ProviderImageCeiling. It is duplicated
// rather than imported because internal/tool is a consumer of this package's
// numbers, not a supplier — the dependency must not run both ways.
const maxInlineImageBytes = 3500 << 10

// validateStream rejects a negative coalescing window (a negative interval
// would read as "never throttle", which is what 0 already means).
func (c Config) validateStream() error {
	if v := c.StreamEmitIntervalMs; v != nil && *v < 0 {
		return fmt.Errorf("config: stream_emit_interval_ms must be >= 0 (0 emits every chunk), got %d", *v)
	}
	return nil
}

// validateStore rejects a segment too small to hold one record. The largest
// record seen in a real store is 128KB; the read tool inlines images as
// base64, bounded by InlineImageBudget, which is itself derived FROM this
// number — so the two cannot drift into a configuration that cannot append.
// Below the floor there is no share of the segment large enough to hold a
// legible picture and the text results beside it.
func (c Config) validateStore() error {
	if s := c.Store.SegmentSize; s != nil && *s < minSegmentSize {
		return fmt.Errorf("config: store.segment_size (%d) must be >= %d: a WAL segment must fit ONE whole record, and a tool_result carrying a base64 image is one record — below the floor there is no share of a segment big enough for a legible picture, and figwal rejects the append (\"payload too large for segment size\") outright", *s, minSegmentSize)
	}
	return nil
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

// PageBudget returns the server-side default page budget in bytes. Nil-safe:
// an agent constructed without config still gets policy from here, so no
// second default can grow somewhere else.
func (l *Loaded) PageBudget() int {
	if l == nil || l.Config.Wire.PageBudget == nil {
		return defaultPageBudget
	}
	return *l.Config.Wire.PageBudget
}

// PageBudgetMax returns the ceiling on a client-requested budget. Nil-safe for
// the same reason, and it matters more here: the ceiling must hold even when
// no config reached us, or a client could make the server materialize an
// unbounded page.
func (l *Loaded) PageBudgetMax() int {
	if l == nil || l.Config.Wire.PageBudgetMax == nil {
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

// Trunks reports whether the trunk capability is enabled. Default true.
func (l *Loaded) Trunks() bool {
	if l.Config.Trunks == nil {
		return true
	}
	return *l.Config.Trunks
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

// CallerIdentityEnabled reports whether the caller-identity authn provider
// is on. Default false — today's behavior.
func (l *Loaded) CallerIdentityEnabled() bool {
	if l.Config.Authz.CallerIdentity == nil {
		return false
	}
	return *l.Config.Authz.CallerIdentity
}

// AuthzPolicy returns the configured policy name, normalized. Empty means
// allow-all.
func (l *Loaded) AuthzPolicy() string {
	switch p := strings.ToLower(strings.TrimSpace(l.Config.Authz.Policy)); p {
	case "", "allow-all", "none", "off":
		return "allow-all"
	default:
		return p
	}
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

// StreamEmitIntervalMs returns the live-emit coalescing window in ms.
// Default 90. Nil-safe: an agent built without config paces the same.
func (l *Loaded) StreamEmitIntervalMs() int {
	if l == nil || l.Config.StreamEmitIntervalMs == nil {
		return 90
	}
	return *l.Config.StreamEmitIntervalMs
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
	if err := cfg.validateStore(); err != nil {
		return nil, err
	}
	if err := cfg.validateStream(); err != nil {
		return nil, err
	}

	return &Loaded{Config: cfg, ConfigDir: configDir, ConfigPath: configPath}, nil
}

func defaultConfig() Config {
	// No DefaultLoadout: empty triggers the first-run flow.
	return Config{}
}
