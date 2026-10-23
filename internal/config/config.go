// Package config loads figaro's configuration from ~/.config/figaro/.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// TelemetryConfig is the [telemetry] table.
type TelemetryConfig struct {
	// Dir is where logs.jsonl and traces.jsonl go; empty or relative resolves against the state dir.
	Dir string `toml:"dir"`

	// Level is the sink floor (debug, info, warn, error); empty means info. FIGARO_LOG_LEVEL wins.
	Level string `toml:"level"`

	// OTLPEndpoint additionally exports logs and traces over OTLP; the file sinks keep working.
	OTLPEndpoint string `toml:"otlp_endpoint"`
}

type Config struct {
	// DefaultOutfit is used when -O is not given; empty triggers the first-run flow.
	DefaultOutfit string `toml:"default_outfit"`

	Telemetry TelemetryConfig `toml:"telemetry"`

	// Trunks enables the trunk capability. Default true.
	Trunks *bool `toml:"trunks"`

	// BundledSkills lets the skills embedded in the binary take part in composing a form. Default true.
	BundledSkills *bool `toml:"bundled_skills"`

	// CLI holds client-owned settings; everything else is the daemon's, which a client changes only through angelus.configure.
	CLI CLIConfig `toml:"cli"`

	Wire    WireConfig    `toml:"wire"`
	Store   StoreConfig   `toml:"store"`
	Memory  MemoryConfig  `toml:"memory"`
	Models  ModelsConfig  `toml:"models"`
	Door    DoorConfig    `toml:"door"`
	Quote   QuoteConfig   `toml:"quote"`
	Authz   AuthzConfig   `toml:"authz"`
	Sandbox SandboxConfig `toml:"sandbox"`

	ServiceTokens map[string]ServiceToken `toml:"service_tokens"`

	Peers  map[string]PeerConfig `toml:"peers"`
	Troupe TroupeConfig          `toml:"troupe"`

	Federation FederationConfig `toml:"federation"`
}

type ServiceToken struct {
	Issuer            string   `toml:"issuer"`
	TokenURL          string   `toml:"token_url"`
	ClientID          string   `toml:"client_id"`
	SecretCredential  string   `toml:"secret_credential"`
	AuthMethod        string   `toml:"auth_method"`
	Audience          []string `toml:"audience"`
	Scopes            []string `toml:"scopes"`
	Grant             string   `toml:"grant"`
	SubjectCredential string   `toml:"subject_credential"`
	SubjectTokenType  string   `toml:"subject_token_type"`
}

// QuoteConfig shapes how a `<lt.block:start-end>!` prompt prefix is rewritten
// into a short quote: enough to recognise the passage, not the passage.
type QuoteConfig struct {
	// HeadChars is the runes kept from the start. Default 480.
	HeadChars *int `toml:"head_chars"`

	// TailChars is the runes kept from the end. Default 160.
	TailChars *int `toml:"tail_chars"`

	// Ellipsis stands between head and tail. Default "…"; a passage within head+tail carries none.
	Ellipsis *string `toml:"ellipsis"`

	// Gutter prefixes every quoted line. Default "> "; "" for none.
	Gutter *string `toml:"gutter"`

	// Header controls the "quoting aria … lt 412.0 · chars 23-1180" line. Default true.
	Header *bool `toml:"header"`
}

// CLIConfig is the client's own settings. Nothing here reaches the daemon.
type CLIConfig struct {
	Troupe string `toml:"troupe"`

	// EchoPrompt: default true.
	EchoPrompt *bool `toml:"echo_prompt"`

	// StatusLine: default true.
	StatusLine *bool `toml:"status_line"`

	// StatusVerbose seeds the status bar's verbosity (^V toggles it per session). Default false.
	StatusVerbose *bool `toml:"status_verbose"`

	// NoticeTTL is how long a notification holds the status bar, in seconds. Default 10; 0 holds until displaced.
	NoticeTTL *int `toml:"notice_ttl"`

	// Interactive selects the TUI first-run wizard over plain numbered prompts. Default true.
	Interactive *bool `toml:"interactive"`

	// CoordFormat is the Go time layout for pager coordinates. Default "02/01/06 15:04:05".
	CoordFormat *string `toml:"coord_format"`

	// StreamCPS is the pacer's target chars/sec; 0 disables pacing.
	StreamCPS *int `toml:"stream_cps"`

	// StreamFirstByteBypassMs is the sync-write window for TTFT. Default 80.
	StreamFirstByteBypassMs *int `toml:"stream_first_byte_bypass_ms"`

	// StreamEmitIntervalMs coalesces live emits; structural changes always emit at once. Default 90; 0 emits every chunk.
	StreamEmitIntervalMs *int `toml:"stream_emit_interval_ms"`

	// CheckUpdates enables the passive startup update hint. Default true.
	CheckUpdates *bool `toml:"check_updates"`

	// UpdateCheckTTLHours bounds how often the proxy is asked. Default 24; 0 disables caching.
	UpdateCheckTTLHours *int `toml:"update_check_ttl_hours"`

	// RefSigil prefixes form references; "@" or ":". Default "@".
	RefSigil string `toml:"ref_sigil"`

	IDs string `toml:"ids"`

	// Editor is a literal command line (no $EDITOR, globbing or substitution) the pager opens a path with.
	Editor *string `toml:"editor"`
}

type StoreConfig struct {
	// SegmentSize bounds one WAL segment file in bytes; applies to new segments only. Default 2 MiB.
	SegmentSize *int `toml:"segment_size"`
}

// MemoryConfig tunes when the daemon reclaims an idle aria.
type MemoryConfig struct {
	// DormantAfterMinutes is the idle time before an aria is reclaimed. Default 15.
	DormantAfterMinutes *int `toml:"dormant_after_minutes"`

	// SweepIntervalSeconds: default 120.
	SweepIntervalSeconds *int `toml:"sweep_interval_seconds"`

	KeepaliveMinutes *int `toml:"keepalive_minutes"`

	ReapedGraceHours *int `toml:"reaped_grace_hours"`

	// MaxLiveArias caps resident agents; 0 (default) is unbounded.
	MaxLiveArias *int `toml:"max_live_arias"`

	// IRWindow bounds resident decoded IR entries per aria; 0 (default) retains everything.
	IRWindow *int `toml:"ir_window"`

	// IRWindowMB bounds resident decoded IR per aria in mebibytes.
	IRWindowMB *int `toml:"ir_window_mb"`

	// TranslationWindowMB bounds resident decoded translations per aria per provider, in mebibytes.
	TranslationWindowMB *int `toml:"translation_window_mb"`

	// UIWindowMB bounds composed UI IR across every materialized aria, in mebibytes.
	// Arias whose records predate turn-id stamps cannot be range-recomposed and stay whole.
	UIWindowMB *int `toml:"ui_window_mb"`

	// SegmentCacheMB bounds raw segment payloads figwal holds across all channels, in mebibytes.
	SegmentCacheMB *int `toml:"segment_cache_mb"`

	// SoftLimitMB is the daemon's GOMEMLIMIT. Default 2048; 0 removes it; GOMEMLIMIT in the environment wins.
	SoftLimitMB *int `toml:"soft_limit_mb"`

	// ActorLingerMS is how long a form's writer goroutine lingers after draining. Default 2000.
	ActorLingerMS *int `toml:"actor_linger_ms"`

	// HandleIdleMinutes is figwal's head-unload window; 0 takes figwal's default, negative never unloads.
	HandleIdleMinutes *int `toml:"handle_idle_minutes"`

	// FormPatchWindow bounds resident decoded patches per form. Default 2048; 0 retains everything.
	FormPatchWindow *int `toml:"form_patch_window"`
}

type ModelsConfig struct {
	PollMinutes *int `toml:"poll_minutes"`
}

const (
	defaultModelsPoll    = 6 * time.Hour
	defaultDormantAfter  = 15 * time.Minute
	defaultSweepInterval = 2 * time.Minute
	defaultKeepalive     = 24 * time.Hour
	defaultReapedGrace   = 7 * 24 * time.Hour
	defaultSegmentSize   = 2 * 1024 * 1024
	minSegmentSize       = 1024 * 1024
)

// Every accessor reads through cfg() and dir() so a nil *Loaded is safe by
// construction; TestEveryLoadedAccessorIsNilSafe enforces it.
func (l *Loaded) cfg() Config {
	if l == nil {
		return Config{}
	}
	return l.Config
}

func (l *Loaded) dir() string {
	if l == nil {
		return ""
	}
	return l.ConfigDir
}

// DormantAfter returns 0 for a non-positive configured value: never reclaim.
func (l *Loaded) DormantAfter() time.Duration {
	if l.cfg().Memory.DormantAfterMinutes == nil {
		return defaultDormantAfter
	}
	if *l.cfg().Memory.DormantAfterMinutes <= 0 {
		return 0
	}
	return time.Duration(*l.cfg().Memory.DormantAfterMinutes) * time.Minute
}

func (l *Loaded) ModelsPoll() time.Duration {
	if l.cfg().Models.PollMinutes == nil {
		return defaultModelsPoll
	}
	if *l.cfg().Models.PollMinutes <= 0 {
		return 0
	}
	return time.Duration(*l.cfg().Models.PollMinutes) * time.Minute
}

func (l *Loaded) ReapedGrace() time.Duration {
	if l.cfg().Memory.ReapedGraceHours == nil {
		return defaultReapedGrace
	}
	if *l.cfg().Memory.ReapedGraceHours <= 0 {
		return 0
	}
	return time.Duration(*l.cfg().Memory.ReapedGraceHours) * time.Hour
}

func (l *Loaded) Keepalive() time.Duration {
	if l.cfg().Memory.KeepaliveMinutes == nil {
		return defaultKeepalive
	}
	if *l.cfg().Memory.KeepaliveMinutes <= 0 {
		return 0
	}
	return time.Duration(*l.cfg().Memory.KeepaliveMinutes) * time.Minute
}

// A negative quote count is returned as written; the quote refuses it by name.
func (l *Loaded) QuoteHeadChars() int { return quoteInt(l.cfg().Quote.HeadChars, QuoteHeadDefault) }
func (l *Loaded) QuoteTailChars() int { return quoteInt(l.cfg().Quote.TailChars, QuoteTailDefault) }
func (l *Loaded) QuoteEllipsis() string {
	if l.cfg().Quote.Ellipsis == nil {
		return QuoteEllipsisDefault
	}
	return *l.cfg().Quote.Ellipsis
}
func (l *Loaded) QuoteGutter() string {
	if l.cfg().Quote.Gutter == nil {
		return QuoteGutterDefault
	}
	return *l.cfg().Quote.Gutter
}
func (l *Loaded) QuoteHeader() bool {
	if l.cfg().Quote.Header == nil {
		return true
	}
	return *l.cfg().Quote.Header
}

const (
	QuoteHeadDefault     = 480
	QuoteTailDefault     = 160
	QuoteEllipsisDefault = "…"
	QuoteGutterDefault   = "> "
)

func quoteInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// SweepInterval is floored at one second so a misconfiguration cannot spin the ticker.
func (l *Loaded) SweepInterval() time.Duration {
	if l.cfg().Memory.SweepIntervalSeconds == nil {
		return defaultSweepInterval
	}
	if *l.cfg().Memory.SweepIntervalSeconds < 1 {
		return time.Second
	}
	return time.Duration(*l.cfg().Memory.SweepIntervalSeconds) * time.Second
}

func (l *Loaded) MaxLiveArias() int {
	if l.cfg().Memory.MaxLiveArias == nil || *l.cfg().Memory.MaxLiveArias < 0 {
		return 0
	}
	return *l.cfg().Memory.MaxLiveArias
}

// IRWindow is 0 for unbounded, else floored at minIRWindow.
func (l *Loaded) IRWindow() int {
	if l.cfg().Memory.IRWindow == nil || *l.cfg().Memory.IRWindow <= 0 {
		return 0
	}
	if w := *l.cfg().Memory.IRWindow; w < minIRWindow {
		return minIRWindow
	}
	return *l.cfg().Memory.IRWindow
}

// IRWindowBytes reports ok=false when unset (the store owns the default);
// an explicit 0 means unbounded and is ok.
func (l *Loaded) IRWindowBytes() (int, bool) {
	if l.cfg().Memory.IRWindowMB == nil {
		return 0, false
	}
	if *l.cfg().Memory.IRWindowMB <= 0 {
		return 0, true
	}
	if mb := *l.cfg().Memory.IRWindowMB; mb < minIRWindowMB {
		return minIRWindowMB << 20, true
	}
	return *l.cfg().Memory.IRWindowMB << 20, true
}

// TranslationWindowBytes has the same unset/explicit-zero contract and floor as IRWindowBytes.
func (l *Loaded) TranslationWindowBytes() (int, bool) {
	if l.cfg().Memory.TranslationWindowMB == nil {
		return 0, false
	}
	if *l.cfg().Memory.TranslationWindowMB <= 0 {
		return 0, true
	}
	if mb := *l.cfg().Memory.TranslationWindowMB; mb < minIRWindowMB {
		return minIRWindowMB << 20, true
	}
	return *l.cfg().Memory.TranslationWindowMB << 20, true
}

func (l *Loaded) TelemetryDir(stateDir string) string {
	if l.cfg().Telemetry.Dir == "" {
		return stateDir
	}
	if filepath.IsAbs(l.cfg().Telemetry.Dir) {
		return l.cfg().Telemetry.Dir
	}
	return filepath.Join(stateDir, l.cfg().Telemetry.Dir)
}

func (l *Loaded) TelemetryLevel() string {
	if l == nil {
		return ""
	}
	return l.cfg().Telemetry.Level
}

func (l *Loaded) TelemetryOTLPEndpoint() string {
	if l == nil {
		return ""
	}
	return l.cfg().Telemetry.OTLPEndpoint
}

// SegmentCacheBytes has the same unset/explicit-zero contract as IRWindowBytes.
func (l *Loaded) SegmentCacheBytes() (int64, bool) {
	if l.cfg().Memory.SegmentCacheMB == nil {
		return 0, false
	}
	if mb := *l.cfg().Memory.SegmentCacheMB; mb > 0 {
		return int64(mb) << 20, true
	}
	return 0, true
}

// UIWindowMB has the same unset/explicit-zero contract as IRWindowBytes.
func (l *Loaded) UIWindowMB() (int, bool) {
	if l.cfg().Memory.UIWindowMB == nil {
		return 0, false
	}
	if mb := *l.cfg().Memory.UIWindowMB; mb > 0 {
		return mb, true
	}
	return 0, true
}

const (
	// A window smaller than one turn makes an in-flight turn re-read its own tail on every append.
	minIRWindow            = 64
	minIRWindowMB          = 1
	defaultSoftLimitMB     = 2048
	defaultActorLingerMS   = 2000
	defaultFormPatchWindow = 2048
)

func (l *Loaded) SoftLimitBytes() int64 {
	if l.cfg().Memory.SoftLimitMB == nil {
		return int64(defaultSoftLimitMB) << 20
	}
	if mb := *l.cfg().Memory.SoftLimitMB; mb > 0 {
		return int64(mb) << 20
	}
	return 0
}

func (l *Loaded) ActorLinger() time.Duration {
	if l.cfg().Memory.ActorLingerMS == nil {
		return time.Duration(defaultActorLingerMS) * time.Millisecond
	}
	ms := *l.cfg().Memory.ActorLingerMS
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (l *Loaded) HandleIdle() time.Duration {
	if l.cfg().Memory.HandleIdleMinutes == nil {
		return 0
	}
	return time.Duration(*l.cfg().Memory.HandleIdleMinutes) * time.Minute
}

func (l *Loaded) FormPatchWindow() int {
	if l.cfg().Memory.FormPatchWindow == nil {
		return defaultFormPatchWindow
	}
	if n := *l.cfg().Memory.FormPatchWindow; n > 0 {
		return n
	}
	return 0
}

func (l *Loaded) SegmentSize() int {
	if l.cfg().Store.SegmentSize == nil {
		return defaultSegmentSize
	}
	return *l.cfg().Store.SegmentSize
}

// Inlined imagery may claim two thirds of a segment; the rest holds the
// tool_result text and envelope, and the whole record must fit one empty segment.
const (
	imageShareNum = 2
	imageShareDen = 3
)

// InlineImageBudget is the base64 byte budget for imagery in one tool_result record.
func (l *Loaded) InlineImageBudget() int {
	budget := l.SegmentSize() / imageShareDen * imageShareNum
	if budget > maxInlineImageBytes {
		budget = maxInlineImageBytes
	}
	return budget
}

// Mirrors tool.ProviderImageCeiling; internal/tool imports this package, not the reverse.
const maxInlineImageBytes = 3500 << 10

func (c Config) validateStream() error {
	if v := c.CLI.StreamEmitIntervalMs; v != nil && *v < 0 {
		return fmt.Errorf("config: stream_emit_interval_ms must be >= 0 (0 emits every chunk), got %d", *v)
	}
	return nil
}

func (c Config) validateStore() error {
	if s := c.Store.SegmentSize; s != nil && *s < minSegmentSize {
		return fmt.Errorf("config: store.segment_size (%d) must be >= %d", *s, minSegmentSize)
	}
	return nil
}

// WireConfig bounds a paginated read: a byte budget paid out in whole nodes,
// never fewer than one.
type WireConfig struct {
	// PageBudget is the default page size in bytes when a client names none. Default 65536.
	PageBudget *int `toml:"page_budget"`

	// PageBudgetMax is clamped onto a client's request. Default 524288.
	PageBudgetMax *int `toml:"page_budget_max"`
}

const (
	defaultPageBudget    = 65536
	defaultPageBudgetMax = 524288
)

func (l *Loaded) PageBudget() int {
	if l.cfg().Wire.PageBudget == nil {
		return defaultPageBudget
	}
	return *l.cfg().Wire.PageBudget
}

func (l *Loaded) PageBudgetMax() int {
	if l.cfg().Wire.PageBudgetMax == nil {
		return defaultPageBudgetMax
	}
	return *l.cfg().Wire.PageBudgetMax
}

// ClampPageBudget: a non-positive request means the server default; any request is capped.
func (l *Loaded) ClampPageBudget(requested int) int {
	if requested <= 0 {
		requested = l.PageBudget()
	}
	if max := l.PageBudgetMax(); requested > max {
		return max
	}
	return requested
}

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

func (l *Loaded) Trunks() bool {
	if l.cfg().Trunks == nil {
		return true
	}
	return *l.cfg().Trunks
}

func (l *Loaded) BundledSkills() bool {
	if l.cfg().BundledSkills == nil {
		return true
	}
	return *l.cfg().BundledSkills
}

func (l *Loaded) EchoPrompt() bool {
	if l.cfg().CLI.EchoPrompt == nil {
		return true
	}
	return *l.cfg().CLI.EchoPrompt
}

func (l *Loaded) StatusLine() bool {
	if l.cfg().CLI.StatusLine == nil {
		return true
	}
	return *l.cfg().CLI.StatusLine
}

func (l *Loaded) StatusVerbose() bool {
	if l.cfg().CLI.StatusVerbose == nil {
		return false
	}
	return *l.cfg().CLI.StatusVerbose
}

func (l *Loaded) NoticeTTL() time.Duration {
	if l.cfg().CLI.NoticeTTL == nil {
		return 10 * time.Second
	}
	return time.Duration(*l.cfg().CLI.NoticeTTL) * time.Second
}

func (l *Loaded) Interactive() bool {
	if l.cfg().CLI.Interactive == nil {
		return true
	}
	return *l.cfg().CLI.Interactive
}

func (l *Loaded) RefSigil() (string, error) {
	s := l.cfg().CLI.RefSigil
	if s == "" {
		return "@", nil
	}
	if s == "@" || s == ":" {
		return s, nil
	}
	return "", fmt.Errorf("config: ref_sigil must be \"@\" or \":\", got %q", s)
}

func (l *Loaded) Editor() string {
	if l.cfg().CLI.Editor == nil {
		return ""
	}
	return strings.TrimSpace(*l.cfg().CLI.Editor)
}

const CoordFormatDefault = "02/01/06 15:04:05"

func (l *Loaded) CoordFormat() string {
	if l.cfg().CLI.CoordFormat == nil || *l.cfg().CLI.CoordFormat == "" {
		return CoordFormatDefault
	}
	return *l.cfg().CLI.CoordFormat
}

func (l *Loaded) IDs() string { return l.cfg().CLI.IDs }

func (l *Loaded) StreamCPS() int {
	if l.cfg().CLI.StreamCPS == nil {
		return 200
	}
	return *l.cfg().CLI.StreamCPS
}

func (l *Loaded) StreamFirstByteBypassMs() int {
	if l.cfg().CLI.StreamFirstByteBypassMs == nil {
		return 80
	}
	return *l.cfg().CLI.StreamFirstByteBypassMs
}

func (l *Loaded) StreamEmitIntervalMs() int {
	if l.cfg().CLI.StreamEmitIntervalMs == nil {
		return 90
	}
	return *l.cfg().CLI.StreamEmitIntervalMs
}

func (l *Loaded) CheckUpdates() bool {
	if l.cfg().CLI.CheckUpdates == nil {
		return true
	}
	return *l.cfg().CLI.CheckUpdates
}

func (l *Loaded) UpdateCheckTTLHours() int {
	if l.cfg().CLI.UpdateCheckTTLHours == nil {
		return 24
	}
	return *l.cfg().CLI.UpdateCheckTTLHours
}

// ProviderAuth lives at providers/<name>.toml. Secret fields are
// AGE-encrypted at rest (AGE-ENC[...]) and must be decrypted through hush.
type ProviderAuth struct {
	APIKey       string `toml:"api_key"`
	APIKeyFile   string `toml:"api_key_file"`
	ServiceToken string `toml:"service_token"`

	AccessToken  string `toml:"access_token"`
	RefreshToken string `toml:"refresh_token"`
	ExpiresAt    int64  `toml:"expires_at"`
}

type Loaded struct {
	Config     Config
	ConfigDir  string
	ConfigPath string
}

func (l *Loaded) ProvidersDir() string {
	return filepath.Join(l.dir(), "providers")
}

func (l *Loaded) ProviderAuthPath(name string) string {
	return filepath.Join(l.ProvidersDir(), name+".toml")
}

func (l *Loaded) TroupesDir() string {
	return filepath.Join(l.dir(), "troupes")
}

func (l *Loaded) OutfitsDir() string {
	return filepath.Join(l.dir(), "outfits")
}

func (l *Loaded) OutfitPath(name string) string {
	files, err := OutfitFiles(l.dir(), nil)
	if err == nil && len(files[name]) > 0 {
		return files[name][0]
	}
	return filepath.Join(l.OutfitsDir(), name+".toml")
}

func (l *Loaded) ListProviders() []string {
	dir := filepath.Join(l.dir(), "providers")
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

func (l *Loaded) ListOutfits() []string {
	files, err := OutfitFiles(l.dir(), nil)
	if err != nil {
		return nil
	}
	return outfitNames(files)
}

// LoadProviderAuth returns nil, nil when the file is absent.
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

// DefaultConfigDir honours FIGARO_CONFIG_DIR as-is, then XDG_CONFIG_HOME/figaro.
func DefaultConfigDir() string {
	if d := os.Getenv("FIGARO_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "figaro")
}

// Load returns defaults when config.toml is missing.
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

	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	if md.IsDefined("authz", "bearer") {
		return nil, fmt.Errorf("%s: %w", configPath, errRetiredBearer)
	}
	// default_loadout is the pre-rename key.
	if cfg.DefaultOutfit == "" {
		var legacy struct {
			DefaultOutfit string `toml:"default_loadout"`
		}
		if err := toml.Unmarshal(data, &legacy); err == nil {
			cfg.DefaultOutfit = legacy.DefaultOutfit
		}
	}
	// CLI keys were top-level before [cli]; a pre-move config.toml still applies.
	var preSection CLIConfig
	if err := toml.Unmarshal(data, &preSection); err == nil {
		c := &cfg.CLI
		if c.EchoPrompt == nil {
			c.EchoPrompt = preSection.EchoPrompt
		}
		if c.StatusLine == nil {
			c.StatusLine = preSection.StatusLine
		}
		if c.Interactive == nil {
			c.Interactive = preSection.Interactive
		}
		if c.CoordFormat == nil {
			c.CoordFormat = preSection.CoordFormat
		}
		if c.StreamCPS == nil {
			c.StreamCPS = preSection.StreamCPS
		}
		if c.StreamFirstByteBypassMs == nil {
			c.StreamFirstByteBypassMs = preSection.StreamFirstByteBypassMs
		}
		if c.StreamEmitIntervalMs == nil {
			c.StreamEmitIntervalMs = preSection.StreamEmitIntervalMs
		}
		if c.CheckUpdates == nil {
			c.CheckUpdates = preSection.CheckUpdates
		}
		if c.RefSigil == "" {
			c.RefSigil = preSection.RefSigil
		}
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
	return Config{}
}

// SetDefaultOutfit rewrites only the outfit key; the rest of the file survives.
func SetDefaultOutfit(configPath, outfitName string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse existing config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	delete(raw, "default_loadout")
	raw["default_outfit"] = outfitName

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(raw)
}
