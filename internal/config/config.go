// Package config loads figaro's configuration from ~/.config/figaro/.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the top-level figaro configuration. Provider/model knobs
// have moved into outfits; this file holds only the chosen outfit
// and CLI-side ergonomics.
type Config struct {
	// DefaultOutfit names the outfit used when -O is not specified.
	// Empty triggers the first-run flow (see rpc.ErrNoDefaultOutfit).
	DefaultOutfit string `toml:"default_outfit"`

	// Trunks enables the trunk capability: a presentation hierarchy an
	// aria can be promoted within, independent of where its history comes
	// from, and a delete that follows it. Default true.
	//
	// With it off, figaro has no trunk pstate at all: the hierarchy IS the
	// fork topology, `fig promote` reports that the server cannot serve it,
	// and a delete can never orphan a survivor because the two agree.
	Trunks *bool `toml:"trunks"`

	// BundledSkills controls whether the skills that ship inside the binary
	// (today: the `figaro` skill, unpacked from the embedded copy) take part
	// in composing a form. Default true.
	//
	// False is for the user who keeps their own `figaro` skill in
	// ~/.config/figaro/skills and does not want an upgrade correcting it.
	// That is the whole trade: bundled skills WIN over a same-named copy in
	// the config dir, precisely so an install can fix a stale one, and this
	// is the switch that says "I will maintain mine myself".
	BundledSkills *bool `toml:"bundled_skills"`

	// CLI holds the settings a client owns: how it echoes, paces and colours
	// its own output. They live in their own [cli] section because everything
	// else here is the SERVER's state, which a client may only ask the daemon
	// to change (angelus.configure) rather than read.
	CLI CLIConfig `toml:"cli"`

	// Wire bounds paginated reads. See WireConfig.
	Wire WireConfig `toml:"wire"`

	// Store bounds the on-disk WAL geometry. See StoreConfig.
	Store StoreConfig `toml:"store"`

	// Memory tunes when the daemon reclaims an idle aria. See MemoryConfig.
	Memory MemoryConfig `toml:"memory"`

	// Authz gates the RPC surface. See AuthzConfig.
	Authz AuthzConfig `toml:"authz"`
}

// CLIConfig is the client's own settings. Nothing here reaches the daemon.
type CLIConfig struct {
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
	// 24. Zero disables caching (each check hits the network): useful
	// only for testing.
	UpdateCheckTTLHours *int `toml:"update_check_ttl_hours"`

	// RefSigil is the prefix character for form references in
	// prompts and tab completion. Must be "@" or ":". Default "@".
	RefSigil string `toml:"ref_sigil"`
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
	// false, every request is anonymous no matter what it presented: the
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
	// lower it to keep individual files small. New segments only: existing
	// ones keep their size and simply stop growing.
	SegmentSize *int `toml:"segment_size"`
}

// MemoryConfig tunes reclamation. Both knobs trade memory against a
// rebuild: reclaiming too eagerly costs the next reader a restore,
// reclaiming too late costs only RSS. The defaults err toward keeping a
// conversation warm as long as someone plausibly comes back to it.
type MemoryConfig struct {
	// DormantAfterMinutes is how long an aria must sit idle: no turn in
	// flight, nothing queued: before the daemon reclaims it. Default 15.
	//
	// Do not set this low. Restore is O(history): ~15 ms to open the xwal
	// head at 600 messages, ~9 ms to rebuild the UI at 10k, ~42 ms at 50k.
	// Paid once it is invisible; paid every thirty seconds it is a flap that
	// costs more than the memory it saves. Zero or negative disables
	// reclamation entirely, which is a debugging setting, not a tuning one.
	DormantAfterMinutes *int `toml:"dormant_after_minutes"`

	// SweepIntervalSeconds is how often the daemon looks. Default 120.
	// Reclamation is never urgent, so this is deliberately slower than the
	// 2-second pid monitor it rides beside.
	SweepIntervalSeconds *int `toml:"sweep_interval_seconds"`

	// MaxLiveArias caps resident agents. 0 (default) is unbounded, which is
	// correct for a desktop: the time rule already bounds a normal session,
	// and a cap set below the working set turns into a restore flap that
	// costs more than the memory it saves.
	//
	// It is a SOFT cap. An aria mid-turn counts toward it and cannot be
	// reclaimed, because the alternative is killing turns to hit a number.
	MaxLiveArias *int `toml:"max_live_arias"`

	// IRWindow bounds how many decoded IR entries stay resident per aria.
	// 0 (default) retains everything, which is the behaviour figaro has always
	// had.
	//
	// The decoded fig IR is the largest thing a live aria holds: 4-5x its
	// encoded bytes, measured at 12.5 MiB on a 2500-message aria, and almost
	// none of it is needed: translation reads the suffix past its watermark,
	// rendering reads recent turns, and backward paging is served from the
	// store by the angelus reader without touching this window.
	//
	// Do not set it below one turn's worth of messages. A turn that appends
	// past the window mid-flight re-reads its own tail from disk, which is
	// correct but pointless. 512 is generous for every observed aria.
	IRWindow *int `toml:"ir_window"`

	// IRWindowMB bounds resident decoded IR per aria in mebibytes, and is the
	// knob to reach for: it bounds the axis that actually costs.
	//
	// Row count does not. Measured on a real 2556-message aria, dropping 80%
	// of ROWS released only 26% of BYTES, a long agentic conversation puts
	// short prose at the head and large tool results at the tail, so a row
	// budget bounds the wrong end of a skewed distribution.
	//
	// 0 (default) is unbounded. 4 holds a comfortable working tail on every
	// aria measured.
	IRWindowMB *int `toml:"ir_window_mb"`

	// SoftLimitMB is the daemon's heap ceiling (Go's GOMEMLIMIT). Go
	// collects harder as it approaches instead of growing to meet whatever
	// the last big sweep asked for, so the ceiling is also a licence: a high
	// one leaves the runtime no reason to give memory back. Default 2048.
	// 0 removes the limit. GOMEMLIMIT in the environment always wins.
	SoftLimitMB *int `toml:"soft_limit_mb"`
}

const (
	defaultDormantAfter  = 15 * time.Minute
	defaultSweepInterval = 2 * time.Minute
	defaultSegmentSize   = 2 * 1024 * 1024
	// minSegmentSize is a HARD floor, not taste: figwal fails an append with
	// "payload too large for segment size" when a single record cannot fit
	// inside one empty segment.
	minSegmentSize = 1024 * 1024
)

// DormantAfter is how long an aria may sit idle before the daemon reclaims
// it. Nil-safe. A non-positive configured value disables reclamation and is
// returned as 0, which every caller reads as "never".
func (l *Loaded) DormantAfter() time.Duration {
	if l == nil || l.Config.Memory.DormantAfterMinutes == nil {
		return defaultDormantAfter
	}
	if *l.Config.Memory.DormantAfterMinutes <= 0 {
		return 0
	}
	return time.Duration(*l.Config.Memory.DormantAfterMinutes) * time.Minute
}

// SweepInterval is how often the reclamation sweep runs. Nil-safe, and
// floored at one second so a misconfiguration cannot spin the ticker.
func (l *Loaded) SweepInterval() time.Duration {
	if l == nil || l.Config.Memory.SweepIntervalSeconds == nil {
		return defaultSweepInterval
	}
	if *l.Config.Memory.SweepIntervalSeconds < 1 {
		return time.Second
	}
	return time.Duration(*l.Config.Memory.SweepIntervalSeconds) * time.Second
}

// MaxLiveArias is the resident-agent cap, or 0 for unbounded. Nil-safe.
func (l *Loaded) MaxLiveArias() int {
	if l == nil || l.Config.Memory.MaxLiveArias == nil || *l.Config.Memory.MaxLiveArias < 0 {
		return 0
	}
	return *l.Config.Memory.MaxLiveArias
}

// IRWindow is the resident decoded-IR cap per aria, or 0 for unbounded.
// Nil-safe, and floored at minIRWindow so a value too small to hold a turn
// cannot be configured: below that the window thrashes against its own
// appends.
func (l *Loaded) IRWindow() int {
	if l == nil || l.Config.Memory.IRWindow == nil || *l.Config.Memory.IRWindow <= 0 {
		return 0
	}
	if w := *l.Config.Memory.IRWindow; w < minIRWindow {
		return minIRWindow
	}
	return *l.Config.Memory.IRWindow
}

// IRWindowBytes is the resident decoded-IR byte budget per aria, or 0 for
// unbounded. Nil-safe, floored for the same reason IRWindow is: a budget too
// small to hold a turn makes an in-flight turn re-read its own tail.
func (l *Loaded) IRWindowBytes() int {
	if l == nil || l.Config.Memory.IRWindowMB == nil {
		return defaultIRWindowMB << 20
	}
	if *l.Config.Memory.IRWindowMB <= 0 {
		return 0 // explicitly unbounded
	}
	if mb := *l.Config.Memory.IRWindowMB; mb < minIRWindowMB {
		return minIRWindowMB << 20
	}
	return *l.Config.Memory.IRWindowMB << 20
}

const (
	// minIRWindow is a floor, not taste: a window smaller than one turn's
	// worth of messages makes an in-flight turn re-read its own tail from disk
	// on every append.
	minIRWindow = 64
	// minIRWindowMB is the same floor in bytes. One turn carrying a couple of
	// large tool results is comfortably under a mebibyte.
	minIRWindowMB = 1
	// defaultSoftLimitMB is the daemon's heap ceiling. Well above a healthy
	// working set (~40 MB fresh, a few hundred with a dozen live arias) so
	// it only bites when something is retaining more than it should.
	defaultSoftLimitMB = 2048
	// defaultIRWindowMB bounds resident decoded IR when nothing says
	// otherwise. It used to be unbounded, and the decoded IR is the largest
	// thing a live aria holds: 4 to 5x its encoded bytes, measured at 12.5
	// MiB on a 2556-message aria and 63 to 86 percent of that aria's whole
	// footprint. Unbounded by default meant every aria anyone touched kept
	// all of it.
	//
	// 4 MiB holds a comfortable working tail on every aria measured. Set
	// ir_window_mb = 0 to go back to unbounded.
	defaultIRWindowMB = 4
)

// SoftLimitBytes is the daemon's heap ceiling in bytes, or 0 for none.
// Nil-safe.
func (l *Loaded) SoftLimitBytes() int64 {
	if l == nil || l.Config.Memory.SoftLimitMB == nil {
		return int64(defaultSoftLimitMB) << 20
	}
	if mb := *l.Config.Memory.SoftLimitMB; mb > 0 {
		return int64(mb) << 20
	}
	return 0
}

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
// than a segment fails the append outright, taking the turn with it: so the
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
// numbers, not a supplier: the dependency must not run both ways.
const maxInlineImageBytes = 3500 << 10

// validateStream rejects a negative coalescing window (a negative interval
// would read as "never throttle", which is what 0 already means).
func (c Config) validateStream() error {
	if v := c.CLI.StreamEmitIntervalMs; v != nil && *v < 0 {
		return fmt.Errorf("config: stream_emit_interval_ms must be >= 0 (0 emits every chunk), got %d", *v)
	}
	return nil
}

// validateStore rejects a segment too small to hold one record. The largest
// record seen in a real store is 128KB; the read tool inlines images as
// base64, bounded by InlineImageBudget, which is itself derived FROM this
// number: so the two cannot drift into a configuration that cannot append.
// Below the floor there is no share of the segment large enough to hold a
// legible picture and the text results beside it.
func (c Config) validateStore() error {
	if s := c.Store.SegmentSize; s != nil && *s < minSegmentSize {
		return fmt.Errorf("config: store.segment_size (%d) must be >= %d: a WAL segment must fit ONE whole record, and a tool_result carrying a base64 image is one record: below the floor there is no share of a segment big enough for a legible picture, and figwal rejects the append (\"payload too large for segment size\") outright", *s, minSegmentSize)
	}
	return nil
}

// WireConfig bounds a paginated read. The budget is spent in BYTES and
// paid out in whole NODES, a page never splits a node, and always
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

// BundledSkills reports whether the binary's own skills take part in
// composing a form. Default true. See Config.BundledSkills, and
// outfit.SetBundledSkills, which this feeds.
func (l *Loaded) BundledSkills() bool {
	if l.Config.BundledSkills == nil {
		return true
	}
	return *l.Config.BundledSkills
}

// EchoPrompt returns whether to echo the prompt. Default true.
func (l *Loaded) EchoPrompt() bool {
	if l.Config.CLI.EchoPrompt == nil {
		return true
	}
	return *l.Config.CLI.EchoPrompt
}

// StatusLine returns whether to show status banners. Default true.
func (l *Loaded) StatusLine() bool {
	if l.Config.CLI.StatusLine == nil {
		return true
	}
	return *l.Config.CLI.StatusLine
}

// Interactive returns whether the first-run wizard should use a rich
// TUI. Default true.
func (l *Loaded) Interactive() bool {
	if l.Config.CLI.Interactive == nil {
		return true
	}
	return *l.Config.CLI.Interactive
}

// CallerIdentityEnabled reports whether the caller-identity authn provider
// is on. Default false: today's behavior.
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

// RefSigil returns the form reference sigil. Default "@".
// Returns an error if the configured value is not "@" or ":".
func (l *Loaded) RefSigil() (string, error) {
	s := l.Config.CLI.RefSigil
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
	if l.Config.CLI.StreamCPS == nil {
		return 200
	}
	return *l.Config.CLI.StreamCPS
}

// StreamFirstByteBypassMs returns the TTFT bypass window. Default 80ms.
func (l *Loaded) StreamFirstByteBypassMs() int {
	if l.Config.CLI.StreamFirstByteBypassMs == nil {
		return 80
	}
	return *l.Config.CLI.StreamFirstByteBypassMs
}

// StreamEmitIntervalMs returns the live-emit coalescing window in ms.
// Default 90. Nil-safe: an agent built without config paces the same.
func (l *Loaded) StreamEmitIntervalMs() int {
	if l == nil || l.Config.CLI.StreamEmitIntervalMs == nil {
		return 90
	}
	return *l.Config.CLI.StreamEmitIntervalMs
}

// CheckUpdates returns whether to run the passive startup update check.
// Default true. Users who prefer silence can set `check_updates = false`
// in ~/.config/figaro/config.toml.
func (l *Loaded) CheckUpdates() bool {
	if l.Config.CLI.CheckUpdates == nil {
		return true
	}
	return *l.Config.CLI.CheckUpdates
}

// UpdateCheckTTLHours returns the update-check cache TTL. Default 24h.
func (l *Loaded) UpdateCheckTTLHours() int {
	if l.Config.CLI.UpdateCheckTTLHours == nil {
		return 24
	}
	return *l.Config.CLI.UpdateCheckTTLHours
}

// ProviderAuth holds credentials for one provider. The on-disk file
// lives at providers/<name>.toml (flat: no per-provider subdirectory).
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
// (providers/<name>.toml: flat, no subdirectory).
func (l *Loaded) ProviderAuthPath(name string) string {
	return filepath.Join(l.ConfigDir, "providers", name+".toml")
}

// OutfitsDir returns the directory housing outfit TOML files.
func (l *Loaded) OutfitsDir() string {
	return filepath.Join(l.ConfigDir, "outfits")
}

// outfitDirs lists where an outfit may live, canonical first (loadouts/ is the
// pre-rename name).
func (l *Loaded) outfitDirs() []string {
	return []string{
		filepath.Join(l.ConfigDir, "outfits"),
		filepath.Join(l.ConfigDir, "loadouts"),
	}
}

// OutfitPath returns the path to a named outfit file.
func (l *Loaded) OutfitPath(name string) string {
	for _, dir := range l.outfitDirs() {
		path := filepath.Join(dir, name+".toml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(l.OutfitsDir(), name+".toml")
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

// ListOutfits returns the names of every outfit file on disk.
func (l *Loaded) ListOutfits() []string {
	seen := map[string]bool{}
	var names []string
	for _, dir := range l.outfitDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".toml") {
				continue
			}
			name = strings.TrimSuffix(name, ".toml")
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
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
	// "figaro" suffix appended): lets dev shells point at an
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
	// default_loadout is the pre-rename key; default_outfit wins when both exist.
	if cfg.DefaultOutfit == "" {
		var legacy struct {
			DefaultOutfit string `toml:"default_loadout"`
		}
		if err := toml.Unmarshal(data, &legacy); err == nil {
			cfg.DefaultOutfit = legacy.DefaultOutfit
		}
	}
	// The CLI settings were read from the TOP LEVEL until they moved into
	// [cli]. Same treatment as default_loadout above, for the same reason: a
	// config.toml written before the move is still on disk, TOML has no idea
	// the key was renamed, and an unread key here is silent: measured,
	// `echo_prompt = false` at the top level yielded EchoPrompt() == true with
	// no error and no warning. A setting that quietly stops applying is worse
	// than one that fails loudly, because nobody goes looking.
	//
	// [cli] wins wherever it says anything; the old spelling only fills what
	// it left unset. Drop this once no config in the wild predates the move.
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
	// No DefaultOutfit: empty triggers the first-run flow.
	return Config{}
}

// SetDefaultOutfit points config.toml at an outfit, preserving everything else
// in the file. The first-run flow drives it through the angelus: a client may
// not write the daemon's config itself.
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
