package rpc

import "encoding/json"



const (
	// Log/stream notifications (server -> client). The wire payload is
	// the serialized Figaro IR: a sealed message is the bare
	// message.Message; the open tail rides a thin envelope. These
	// supersede the stream.* vocabulary below.
	MethodLogEntry = "log.entry" // a sealed message.Message at its durable index
	MethodLogOpen  = "log.open"  // the open (unsealed) tail message, full state
	MethodLogPatch = "log.patch" // a delta against the open tail (delta mode)
	MethodLogAbort = "log.abort" // the open tail was burned (interrupt/fault/exit)
	MethodTurnDone = "turn.done" // the turn went idle

	// Deprecated: the stream.* lifecycle vocabulary, retired in favor
	// of the log.* frames above. Kept until the producer/consumer
	// cutover removes their last references.
	MethodDelta            = "stream.delta"
	MethodToolOutput       = "stream.tool_output"
	MethodThinking         = "stream.thinking"
	MethodToolInvokeStart  = "stream.tool_invoke_start"
	MethodToolInvokeDelta  = "stream.tool_invoke_delta"
	MethodToolInvokeReady  = "stream.tool_invoke_ready"
	MethodToolStart        = "stream.tool_start"
	MethodToolEnd          = "stream.tool_end"
	MethodMessageEnd       = "stream.message_end"
	MethodMessage          = "stream.message"
	MethodDone             = "stream.done"
	MethodError            = "stream.error"

	// Requests.
	MethodQua        = "figaro.qua"
	MethodRead       = "figaro.read"
	MethodContext    = "figaro.context"
	MethodInterrupt  = "figaro.interrupt"
	MethodSet        = "figaro.set"
	MethodLoadout    = "figaro.loadout"
	MethodChalkboard = "figaro.chalkboard"
)

// Typed JSON-RPC error codes for figaro. The -32000..-32099 range
// is reserved by JSON-RPC 2.0 for application errors.
const (
	// ErrNoDefaultLoadout: config.toml has no default_loadout and the
	// request omitted one. Data: ErrorData{AvailableProviders}.
	ErrNoDefaultLoadout = -32010

	// ErrNoProvider: resolved loadout has no system.provider key.
	// Data: ErrorData{AvailableProviders, Loadout}.
	ErrNoProvider = -32011

	// ErrLoadoutNotFound: named loadout is not on disk.
	// Data: ErrorData{Name, SearchPaths}.
	ErrLoadoutNotFound = -32012
)

// ErrorData is the structured payload attached to typed JSON-RPC errors.
type ErrorData struct {
	AvailableProviders []string `json:"available_providers,omitempty"`
	Loadout            string   `json:"loadout,omitempty"`
	Name               string   `json:"name,omitempty"`
	SearchPaths        []string `json:"search_paths,omitempty"`
}



const (
	MethodCreate      = "figaro.create"
	MethodKill        = "figaro.kill"
	MethodList        = "figaro.list"
	MethodAttach      = "figaro.attach"
	MethodAngelusInfo = "angelus.info"

	MethodBind    = "pid.bind"
	MethodResolve = "pid.resolve"
	MethodUnbind  = "pid.unbind"

	// MethodAriaRead returns IR entries for an aria, serving through
	// the angelus's shared LogCache so live writes and reads don't
	// race across processes.
	MethodAriaRead = "aria.read"

	MethodStatus       = "angelus.status"
	MethodSaveBindings = "angelus.save_bindings"
)

// QuaRequest is the prompt call with optional chalkboard input.
type QuaRequest struct {
	Text       string           `json:"text"`
	Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"`

	// DeltaMode opts this connection's live stream into PatchEntry
	// deltas for the open tail instead of full OpenEntry re-sends.
	DeltaMode bool `json:"delta_mode,omitempty"`
}

// ChalkboardInput carries an optional state update.
type ChalkboardInput struct {
	Context map[string]json.RawMessage `json:"context,omitempty"`
	Patch   *ChalkboardPatch           `json:"patch,omitempty"`
}

// ChalkboardPatch is the wire shape for a chalkboard delta.
type ChalkboardPatch struct {
	Set    map[string]json.RawMessage `json:"set,omitempty"`
	Remove []string                   `json:"remove,omitempty"`
}

type QuaResponse struct {
	OK bool `json:"ok"`

	// Index is the durable LT the appended user tic is expected to
	// occupy. On an idle aria it is exact; with prompts already queued
	// it is the index at enqueue time plus the queue depth. Clients
	// that need an exact cursor should figaro.read instead of trusting
	// this for anything load-bearing.
	Index uint64 `json:"index"`
}

// ReadRequest reads the aria log from an index, optionally following
// live. See the stream respec §3.3.
type ReadRequest struct {
	From      uint64 `json:"from,omitempty"`       // inclusive start; 0 = beginning
	Last      uint64 `json:"last,omitempty"`       // relative window: last N messages (overrides From)
	Limit     uint64 `json:"limit,omitempty"`      // max sealed messages this batch; 0 = server max
	Follow    bool   `json:"follow,omitempty"`     // keep open and stream new entries live
	DeltaMode bool   `json:"delta_mode,omitempty"` // open tail as PatchEntry deltas (only with Follow)
}

// ReadResponse is the catch-up batch returned by figaro.read. With
// Follow, further entries arrive as log.* notifications afterward.
type ReadResponse struct {
	Entries  []LogEntry `json:"entries"`            // sealed messages, ascending by index
	Open     *OpenEntry `json:"open,omitempty"`     // present iff the tail is mid-stream and in range
	NextFrom uint64     `json:"next_from"`          // resume cursor; == tail+1 when caught up
	Tail     uint64     `json:"tail"`               // highest sealed index at read time
	Live     bool       `json:"live"`               // true iff a turn is currently streaming
}

type InterruptRequest struct{}

type InterruptResponse struct {
	OK bool `json:"ok"`
}

type ContextRequest struct{}

type ContextResponse struct {
	Messages []interface{} `json:"messages"` // []message.Message, but interface{} for serialization flexibility
}

// SetRequest applies a chalkboard patch directly.
type SetRequest struct {
	Patch ChalkboardPatch `json:"patch"`
}

type SetResponse struct {
	OK     bool     `json:"ok"`
	Set    []string `json:"set,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// LoadoutRequest names a loadout to apply additively to the aria's
// current chalkboard. Keys with values equal to the current snapshot
// are skipped; no removals are performed.
type LoadoutRequest struct {
	Name string `json:"name"`
}

// LoadoutResponse lists the keys created or updated.
type LoadoutResponse struct {
	OK  bool     `json:"ok"`
	Set []string `json:"set,omitempty"`
}

// ChalkboardResponse returns the agent's current snapshot.
type ChalkboardResponse struct {
	Snapshot map[string]json.RawMessage `json:"snapshot"`
}

type FigaroInfoResponse struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	MessageCount     int    `json:"message_count"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`  // cumulative cache-hit tokens
	CacheWriteTokens int    `json:"cache_write_tokens"` // cumulative cache-write tokens
	ContextTokens    int    `json:"context_tokens"`     // estimated next-turn input size
	ContextExact     bool   `json:"context_exact"`      // true if from Usage watermark
	CreatedAt        int64  `json:"created_at"`         // unix millis
	LastActive       int64  `json:"last_active"`        // unix millis
	BoundPIDs        []int  `json:"bound_pids"`
}

// CreateRequest names the loadout for a new aria. ID is optional;
// empty = auto-generated.
type CreateRequest struct {
	ID        string           `json:"id,omitempty"`
	Loadout   string           `json:"loadout,omitempty"`
	Patch     *ChalkboardPatch `json:"patch,omitempty"`
	Ephemeral bool             `json:"ephemeral,omitempty"`
}

type CreateResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// Endpoint describes how to connect to a figaro.
type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
}

type KillRequest struct {
	FigaroID string `json:"figaro_id"`
}

type KillResponse struct {
	OK bool `json:"ok"`
}

// AttachRequest restores a dormant aria without binding a pid.
type AttachRequest struct {
	FigaroID string `json:"figaro_id"`
}

type AttachResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

type ListResponse struct {
	Figaros []FigaroInfoResponse `json:"figaros"`
}

type BindRequest struct {
	PID      int    `json:"pid"`
	FigaroID string `json:"figaro_id"`
}

type BindResponse struct {
	OK bool `json:"ok"`
}

type ResolveRequest struct {
	PID int `json:"pid"`
}

type ResolveResponse struct {
	FigaroID string   `json:"figaro_id,omitempty"`
	Endpoint Endpoint `json:"endpoint,omitempty"`
	Found    bool     `json:"found"`
}

type UnbindRequest struct {
	PID int `json:"pid"`
}

type UnbindResponse struct {
	OK bool `json:"ok"`
}

type StatusResponse struct {
	Uptime      int64 `json:"uptime_ms"` // millis since angelus start
	FigaroCount int   `json:"figaro_count"`
	BoundPIDs   int   `json:"bound_pids"`
}

type SaveBindingsResponse struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"` // number of bindings written
}

// AriaReadRequest names the aria and the window of entries to return.
// From is inclusive; Limit==0 means "no upper bound". The angelus
// caps responses to a sensible upper bound regardless.
type AriaReadRequest struct {
	FigaroID string `json:"figaro_id"`
	From     uint64 `json:"from,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// AriaReadEntry is one IR entry on the wire, with LT separated from
// the payload so clients can ignore the figaro-internal envelope.
type AriaReadEntry struct {
	LT      uint64          `json:"lt"`
	Payload json.RawMessage `json:"payload"`
}

type AriaReadResponse struct {
	Entries  []AriaReadEntry `json:"entries"`
	Total    int             `json:"total"`               // total entries in the aria
	NextFrom uint64          `json:"next_from,omitempty"` // 0 when no more
}
