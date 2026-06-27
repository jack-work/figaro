package rpc

import "encoding/json"

const (
	// Live-render notifications (server -> client). Each unit is one
	// markdown blob mutated by single-region splices; the renderer (CLI
	// or web) turns the blob into rows/DOM.
	MethodLogSnapshot = "log.snapshot" // establish the current live unit's full node list
	MethodNodeOpen    = "node.open"    // append a node to the live unit
	MethodNodePatch   = "node.patch"   // splice a node's streamed string field
	MethodNodeSet     = "node.set"     // update a tool node's scalar status
	MethodLogCommit   = "log.commit"   // freeze the live unit; next is a new one
	MethodTurnDone    = "turn.done"    // the turn went idle

	// Requests.
	MethodQua        = "figaro.qua"
	MethodContext    = "figaro.context"
	MethodInterrupt  = "figaro.interrupt"
	MethodSet        = "figaro.set"
	MethodLoadout    = "figaro.loadout"
	MethodChalkboard = "figaro.chalkboard"

	// MethodRead returns the conversation so far as committed unit blobs
	// plus the in-flight live unit, so a freshly-connected client can
	// rebuild scrollback and then follow live log.* frames on the same
	// (already-subscribed) connection.
	MethodRead = "figaro.read"
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
	MethodFork        = "figaro.fork"
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

// ReadRequest is the (currently empty) catch-up request; the whole
// conversation is returned.
type ReadRequest struct{}

// ReadResponse carries the catch-up batch: committed units to flush to
// scrollback, then the in-flight live unit (nil when idle) to seed the
// live region before following log.* frames.
type ReadResponse struct {
	Committed []SnapshotEntry `json:"committed,omitempty"`
	Live      *SnapshotEntry  `json:"live,omitempty"`
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
	Mantra           string `json:"mantra"`             // agent-maintained essence phrase (chalkboard "mantra")
	Cwd              string `json:"cwd"`                // working directory (chalkboard "system.cwd")
	BoundPIDs        []int  `json:"bound_pids"`
}

// CreateRequest names the loadout for a new aria. The system mints the
// aria id; callers cannot choose it.
type CreateRequest struct {
	Loadout   string           `json:"loadout,omitempty"`
	Patch     *ChalkboardPatch `json:"patch,omitempty"`
	Ephemeral bool             `json:"ephemeral,omitempty"`
}

type CreateResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// ForkRequest branches a conversation at its head.
type ForkRequest struct {
	FigaroID string `json:"figaro_id"`
}

// ForkResponse returns the two fresh child ids. The parent freezes and
// keeps its id as a navigable (read-only) index node.
type ForkResponse struct {
	Parent       string `json:"parent"`
	Continuation string `json:"continuation"`
	Alternative  string `json:"alternative"`
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
