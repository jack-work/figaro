package rpc

import (
	"time"
)

// OutfitLayer with no Name is the synthetic root holding several requested
// outfits side by side.
type OutfitLayer struct {
	Name   string         `json:"name,omitempty"`
	Path   string         `json:"path,omitempty"`
	Found  bool           `json:"found"`
	Cycle  bool           `json:"cycle,omitempty"`
	Layers []*OutfitLayer `json:"layers,omitempty"`
}

// Spec is the `-O` syntax; empty means the configured default.
type OutfitsRequest struct {
	Spec string `json:"spec,omitempty"`
}

// Flagged is false when nothing was minted yet.
type OutfitReloadResponse struct {
	Flagged bool   `json:"flagged"`
	FormID  string `json:"form_id,omitempty"`
}

// Closure lists found and missing nodes alike.
type OutfitsResponse struct {
	Default string       `json:"default,omitempty"`
	Names   []string     `json:"names,omitempty"`
	Closure *OutfitLayer `json:"closure,omitempty"`
}

// ConfigureRequest patches the server's config.toml.
type ConfigureRequest struct {
	// DefaultOutfit sets config.default_outfit; empty leaves it alone.
	DefaultOutfit string `json:"default_outfit,omitempty"`
	// Outfit writes outfits/<name>.toml from Body, never clobbering an
	// existing file. Both must be set together.
	Outfit string `json:"outfit,omitempty"`
	Body   string `json:"body,omitempty"`
	// Refresh re-reads config.toml and drops every cached outfit fold.
	Refresh bool `json:"refresh,omitempty"`
}

type ConfigureResponse struct {
	DefaultOutfit string `json:"default_outfit,omitempty"`
	OutfitPath    string `json:"outfit_path,omitempty"`
	Refreshed     bool   `json:"refreshed,omitempty"`
}

type StatusResponse struct {
	Uptime      int64 `json:"uptime_ms"` // millis since angelus start
	FigaroCount int   `json:"figaro_count"`
	BoundPIDs   int   `json:"bound_pids"`
	// Build is the daemon's VCS revision; the wire changes between builds.
	Build string `json:"build,omitempty"`

	// Mem is a pointer so an older daemon's silence reads as unknown, not zero.
	Mem *MemStatus `json:"mem,omitempty"`
}

type MemStatus struct {
	LiveArias     int `json:"live_arias"`     // agents in the registry
	ResidentArias int `json:"resident_arias"` // aria handles cached in the backend
	Sessions      int `json:"sessions"`       // backgrounded exec sessions, all arias
	Goroutines    int `json:"goroutines"`

	// Endpoints and AttachedClients are independent of LiveArias: a reclaimed
	// aria keeps its socket and attached shells.
	Endpoints       int `json:"endpoints"`
	AttachedClients int `json:"attached_clients"`

	ResidentIRRows      int `json:"resident_ir_rows"`
	ResidentFormPatches int `json:"resident_form_patches"`
	ResidentIRBytes     int `json:"resident_ir_bytes"`
	// One translation cache per (aria, provider); nothing bounds it.
	ResidentTranslationRows  int `json:"resident_translation_rows"`
	ResidentTranslationBytes int `json:"resident_translation_bytes"`
	LoadedHeads              int `json:"loaded_heads"`
	// Raw segment payloads figwal holds, against [memory] segment_cache_mb.
	// The IR, translation and patch numbers above are decoded copies of these.
	SegmentCacheBytes  int64 `json:"segment_cache_bytes"`
	SegmentCacheBudget int64 `json:"segment_cache_budget"`
	// SegmentCacheLoads climbing with reads rather than distinct segments
	// means blocks are dropped as fast as they are built.
	SegmentCacheLoads int64 `json:"segment_cache_loads"`
	// Composed UI IR resident, against [memory] ui_window_mb; an evicted turn
	// keeps its index and recomposes on the next read into it.
	UIWindowBytes     int64 `json:"ui_window_bytes,omitempty"`
	UIWindowBudget    int64 `json:"ui_window_budget,omitempty"`
	UIWindowEvictions int   `json:"ui_window_evictions,omitempty"`
	Librettos         int   `json:"librettos"`
	LibrettoObservers int   `json:"libretto_observers"`
	// Boot reconciliation results.
	LibrettoSweepMinted    int `json:"libretto_sweep_minted"`
	LibrettoSweepCorrected int `json:"libretto_sweep_corrected"`
	LibrettoSweepMissing   int `json:"libretto_sweep_missing"`

	// HeapAllocBytes is not the live set: it includes garbage not yet swept,
	// and an idle daemon never triggers the sweep. Read it after a GC.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"` // spans in use, incl. fragmentation
	// RSS ~= HeapSys - HeapReleased + stacks + runtime metadata + binary.
	HeapIdleBytes     uint64 `json:"heap_idle_bytes"`
	HeapReleasedBytes uint64 `json:"heap_released_bytes"`
	HeapSysBytes      uint64 `json:"heap_sys_bytes"`
	SysBytes          uint64 `json:"sys_bytes"`
	NumGC             uint32 `json:"num_gc"`
	MemLimitBytes     int64  `json:"mem_limit_bytes"` // armed GOMEMLIMIT; MaxInt64 = unlimited

	// Collected is set only when the daemon ran a GC before reporting.
	Collected *Collection `json:"collected,omitempty"`

	PprofSocket string `json:"pprof_socket,omitempty"` // empty when not armed
}

// Collection is a forced GC's before and after: the only live-set reading
// available without a profiler.
type Collection struct {
	BeforeBytes   uint64 `json:"before_bytes"`
	AfterBytes    uint64 `json:"after_bytes"`
	ReclaimedByes uint64 `json:"reclaimed_bytes"`
	// ReleasedDelta is what the scavenger handed back during the same call;
	// RSS may stay high for minutes after a forced GC.
	ReleasedDelta uint64        `json:"released_delta_bytes"`
	Took          time.Duration `json:"took_ns"`
}

type SaveBindingsResponse struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"`
}

// Aria filters to one conversation; Limit takes the newest N (0 for all).
type ProviderLedgerRequest struct {
	Aria  string `json:"aria,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type ProviderLedgerResponse struct {
	Rounds []ProviderRound `json:"rounds"`
	// Retained is how many rows the ring holds at all.
	Retained int `json:"retained"`
}

// ProviderRound appears the moment the request departs, with InFlight set,
// and is completed in place.
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

	RetryAfterS int64             `json:"retry_after_s,omitempty"` // seconds, as sent
	RateLimit   map[string]string `json:"rate_limit,omitempty"`
	Err         string            `json:"err,omitempty"`

	// Stream is set for a websocket round: Method reads WS, Status stays 0,
	// and the outcome is Stream.Status.
	Stream *StreamStats `json:"stream,omitempty"`
}

// StreamStats carries counts, sizes, timings and bounded tokens only: no
// error text, headers, payloads or item identifiers.
type StreamStats struct {
	Status   string `json:"status,omitempty"` // "" while the stream is running
	ErrClass string `json:"err_class,omitempty"`

	Events        int64 `json:"events"`
	UnknownEvents int64 `json:"unknown_events,omitempty"`
	RespBytes     int64 `json:"resp_bytes,omitempty"`

	FirstEventMS    int64 `json:"first_event_ms,omitempty"`
	FirstToolMS     int64 `json:"first_tool_ms,omitempty"`
	FirstArgumentMS int64 `json:"first_argument_ms,omitempty"`

	ArgumentDeltas int64 `json:"argument_deltas,omitempty"`
	ArgumentBytes  int64 `json:"argument_bytes,omitempty"`
	// ArgumentUnmatched counts fragments naming a tool call the accumulator
	// had no record of.
	ArgumentUnmatched int64 `json:"argument_unmatched,omitempty"`

	EventTypes   map[string]int64 `json:"event_types,omitempty"`
	TypesDropped int64            `json:"types_dropped,omitempty"`
}
