package rpc

// ADMINISTRATION: daemon status, outfits, configuration, ledgers.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import ()

// OutfitLayer is one node of an outfit's layer closure. A node with no Name is
// the synthetic root that holds several requested outfits side by side.
type OutfitLayer struct {
	Name   string         `json:"name,omitempty"`
	Path   string         `json:"path,omitempty"`
	Found  bool           `json:"found"`
	Cycle  bool           `json:"cycle,omitempty"`
	Layers []*OutfitLayer `json:"layers,omitempty"`
}

// OutfitsRequest asks what outfits exist, and how Spec composes. Spec is the
// `-O` syntax; empty means the configured default.
type OutfitsRequest struct {
	Spec string `json:"spec,omitempty"`
}

// OutfitsResponse is the outfits on disk, the configured default, and the layer
// closure of the requested spec: found and missing nodes alike, because a
// broken reference is best explained by the shape it was found in.
// OutfitReloadResponse: Flagged is false when nothing was minted yet
// (the next fig new computes from files regardless).
type OutfitReloadResponse struct {
	Flagged bool   `json:"flagged"`
	FormID  string `json:"form_id,omitempty"`
}

type OutfitsResponse struct {
	Default string       `json:"default,omitempty"`
	Names   []string     `json:"names,omitempty"`
	Closure *OutfitLayer `json:"closure,omitempty"`
}

// ConfigureRequest patches the server's config.toml. Only the keys the wizard
// needs are addressable: a client may point the daemon at an outfit and write
// the starter outfit itself, and nothing else.
type ConfigureRequest struct {
	// DefaultOutfit sets config.default_outfit. Empty leaves it alone.
	DefaultOutfit string `json:"default_outfit,omitempty"`
	// Outfit writes outfits/<name>.toml from Body, refusing to clobber an
	// existing file. Both must be set together.
	Outfit string `json:"outfit,omitempty"`
	Body   string `json:"body,omitempty"`
	// Refresh re-reads config.toml and drops every cached outfit fold, so an
	// outfit edited or added by hand is picked up without a daemon restart.
	// Config is otherwise read once, at start.
	Refresh bool `json:"refresh,omitempty"`
}

// ConfigureResponse reports what the server wrote.
type ConfigureResponse struct {
	DefaultOutfit string `json:"default_outfit,omitempty"`
	OutfitPath    string `json:"outfit_path,omitempty"`
	Refreshed     bool   `json:"refreshed,omitempty"`
}

type StatusResponse struct {
	Uptime      int64 `json:"uptime_ms"` // millis since angelus start
	FigaroCount int   `json:"figaro_count"`
	BoundPIDs   int   `json:"bound_pids"`
	// Build is the daemon's VCS revision. A CLI speaking a different build
	// than the running daemon renders nothing at all: the wire changes
	// between builds: so the mismatch must be named, not suffered.
	Build string `json:"build,omitempty"`

	// Mem is a pointer so an older daemon's silence reads as "cannot
	// say" rather than zero.
	Mem *MemStatus `json:"mem,omitempty"`
}

// MemStatus is what the daemon can say about its footprint with no
// profiler attached. The numbers that motivated aria eviction were
// observations of RSS, not attributions: nothing distinguished a backend
// holding decoded logs from an agent holding a composed UI.
type MemStatus struct {
	LiveArias     int `json:"live_arias"`     // agents in the registry; eviction may not touch these
	ResidentArias int `json:"resident_arias"` // aria handles cached in the backend; agents pin them
	Sessions      int `json:"sessions"`       // backgrounded exec sessions, all arias
	Goroutines    int `json:"goroutines"`     // per-aria goroutines should fall when arias do

	// Endpoints is open aria sockets, and AttachedClients the connections
	// across them. Both are independent of LiveArias by design: an endpoint
	// with clients and no agent is a reclaimed aria whose shells are still
	// attached, which is the state hibernation exists to produce.
	Endpoints       int `json:"endpoints"`
	AttachedClients int `json:"attached_clients"`

	// ResidentIRRows is decoded IR entries held across every open aria. It is
	// the number the IR window bounds, and the one that moves when the window
	// is doing anything at all.
	ResidentIRRows int `json:"resident_ir_rows"`
	// ResidentFormPatches is decoded form patches held across every open
	// form. Bounded by form_patch_window; the store's other retention.
	ResidentFormPatches int `json:"resident_form_patches"`
	// ResidentIRBytes is the estimated retained size of those rows, which is
	// the number that correlates with RSS. Rows are a poor proxy: the large
	// entries cluster at the tail of a conversation.
	ResidentIRBytes int `json:"resident_ir_bytes"`
	// ResidentTranslationRows and ResidentTranslationBytes are the OTHER
	// cache: one per (aria, provider), holding the provider's wire form of
	// every message it has translated. Nothing bounds it and, until these
	// two, nothing counted it either, so a daemon holding hundreds of
	// megabytes of translations reported only its IR.
	ResidentTranslationRows  int `json:"resident_translation_rows"`
	ResidentTranslationBytes int `json:"resident_translation_bytes"`
	// LoadedHeads is how many lineage heads figwal holds open. A head used to
	// cost a whole channel's raw payloads, which made it the biggest resident
	// thing in the daemon; it now costs the channel's INDEX, and payloads are
	// loaded per segment on demand against SegmentCacheBytes below.
	LoadedHeads int `json:"loaded_heads"`
	// SegmentCacheBytes is the raw segment payloads figwal holds across every
	// open channel, and SegmentCacheBudget the bound they are held against
	// ([memory] segment_cache_mb). This is the layer every other cache here
	// sits on: the IR, translation and patch numbers above count DECODED
	// copies of these same bytes.
	SegmentCacheBytes  int64 `json:"segment_cache_bytes"`
	SegmentCacheBudget int64 `json:"segment_cache_budget"`
	// SegmentCacheLoads counts whole-segment loads. Climbing with READS
	// rather than with distinct segments is the alarm: blocks are being
	// dropped as fast as they are built.
	SegmentCacheLoads int64 `json:"segment_cache_loads"`
	// UIWindowBytes is the composed UI IR resident across every
	// materialized aria, held against UIWindowBudget ([memory]
	// ui_window_mb); UIWindowEvictions counts turns hollowed to stay
	// under it. A turn evicted keeps its index and recomposes on the
	// read that lands in it.
	UIWindowBytes     int64 `json:"ui_window_bytes,omitempty"`
	UIWindowBudget    int64 `json:"ui_window_budget,omitempty"`
	UIWindowEvictions int   `json:"ui_window_evictions,omitempty"`
	// Librettos is how many derived forms are open and folding, and
	// LibrettoObservers how many figaros they carry between them. A fold is
	// a goroutine and a subscription; this is what studying costs.
	Librettos         int `json:"librettos"`
	LibrettoObservers int `json:"libretto_observers"`
	// The boot reconciliation's result: what it repaired and what it could
	// not. A migration that happens in the background must be observable
	// without stopping the daemon to ask.
	LibrettoSweepMinted    int `json:"libretto_sweep_minted"`
	LibrettoSweepCorrected int `json:"libretto_sweep_corrected"`
	LibrettoSweepMissing   int `json:"libretto_sweep_missing"`

	HeapAllocBytes uint64 `json:"heap_alloc_bytes"` // live heap objects
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"` // spans in use, incl. fragmentation
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`   // heap reserved from the OS
	SysBytes       uint64 `json:"sys_bytes"`        // total from the OS, all arenas
	NumGC          uint32 `json:"num_gc"`
	MemLimitBytes  int64  `json:"mem_limit_bytes"` // armed GOMEMLIMIT; MaxInt64 = unlimited

	PprofSocket string `json:"pprof_socket,omitempty"` // empty when not armed
}

type SaveBindingsResponse struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"` // number of bindings written
}

// ProviderLedgerRequest reads recent provider HTTP round-trips from the
// daemon. Aria filters to one conversation; Limit takes the newest N (0 for
// everything retained).
type ProviderLedgerRequest struct {
	Aria  string `json:"aria,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type ProviderLedgerResponse struct {
	Rounds []ProviderRound `json:"rounds"`
	// Retained is how many rows the ring holds at all, so a caller can tell
	// "nothing happened" from "it happened before we started remembering".
	Retained int `json:"retained"`
}

// ProviderRound is one provider HTTP round-trip as the daemon's transport saw
// it. A row appears the moment the request departs, with InFlight set, and is
// completed in place: a request that never returns is the interesting one and
// tracing cannot show it, because spans export on end.
type ProviderRound struct {
	Seq  uint64 `json:"seq"`
	Aria string `json:"aria,omitempty"`

	Method string `json:"method"`
	URL    string `json:"url"`

	StartedAtMS int64 `json:"started_at_ms"`
	DurationMS  int64 `json:"duration_ms"`
	InFlight    bool  `json:"in_flight"`

	Status    int    `json:"status,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	ReqBytes  int64  `json:"req_bytes,omitempty"`

	// RetryAfterS is the wait the provider asked for, in seconds, exactly as
	// sent. On a 429 this is the whole explanation of an apparent hang.
	RetryAfterS int64             `json:"retry_after_s,omitempty"`
	RateLimit   map[string]string `json:"rate_limit,omitempty"`
	Err         string            `json:"err,omitempty"`
}
