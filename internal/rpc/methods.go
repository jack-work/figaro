package rpc

import "encoding/json"

const (
	// Live-render wire (server -> client). The conversation is delivered as
	// aria reads: MethodAriaFrame pushes them live (server-pushed pagination),
	// and MethodRead pulls one for catch-up from a figaro LT. Both carry an
	// aria.AriaRead. MethodTurnDone is the one control signal (turn went idle).
	MethodAriaFrame = "figaro.aria" // push one aria read (committed + live delta)
	MethodTurnDone  = "turn.done"   // the turn went idle

	// Requests.
	MethodQua        = "figaro.qua"
	MethodContext    = "figaro.context"
	MethodInterrupt  = "figaro.interrupt"
	MethodSet        = "figaro.set"
	MethodLoadout    = "figaro.loadout"
	MethodChalkboard = "figaro.chalkboard"

	// MethodRead pulls one aria read caught up from a figaro LT (the
	// catch-up half of the same paginated read the MethodAriaFrame stream
	// pushes), so a (re)connecting client can rebuild from its cursor and
	// then follow the live frames on the same connection.
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
	MethodPromote     = "figaro.promote"
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
	// Cursor is the highest committed figaro LT at the moment the prompt was
	// accepted. The client streams from here: everything after it (the prompt's
	// own turn, or the live turn it steers) arrives as updates, and the stream
	// ends on the turn.done that reports the agent idle.
	Cursor int `json:"cursor"`
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
// conversation is returned. The result is an aria.AriaRead caught up from
// SinceLT (a figaro LT cursor; 0 = from the beginning).
type ReadRequest struct {
	SinceLT int `json:"sinceLT,omitempty"`
}

type FigaroInfoResponse struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	MessageCount     int    `json:"message_count"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`      // cumulative cache-hit tokens
	CacheWriteTokens int    `json:"cache_write_tokens"`     // cumulative cache-write tokens
	ContextTokens    int    `json:"context_tokens"`         // estimated next-turn input size
	ContextExact     bool   `json:"context_exact"`          // true if from Usage watermark
	CreatedAt        int64  `json:"created_at"`             // unix millis
	LastActive       int64  `json:"last_active"`            // unix millis
	Mantra           string `json:"mantra"`                 // agent-maintained essence phrase (chalkboard "mantra")
	Cwd              string `json:"cwd"`                    // working directory (chalkboard "system.cwd")
	LoadoutName      string `json:"loadout_name,omitempty"` // chalkboard system.loadout_name
	LoadoutVer       string `json:"loadout_ver,omitempty"`  // "live" if the stamped hash matches the current loadout, else its short hash
	BoundPIDs        []int  `json:"bound_pids"`

	// Fork-forest position (conversation nodes). Vector is the
	// child-index path (0, 0.0, 0.1, …); Trunk is the thread id that
	// flows down the continuation line; Parent is the node forked from;
	// Frozen marks a fork point (read-only index node).
	Vector     []int  `json:"vector,omitempty"`
	Trunk      string `json:"trunk,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Frozen     bool   `json:"frozen,omitempty"`
	BranchedLT uint64 `json:"branched_lt,omitempty"` // main-LT this trunk diverged at
	Kind       string `json:"kind,omitempty"`        // "conversation" | "loadout" | "null" (set in global listings)
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

// ForkRequest branches a conversation. AtMainLT == 0 forks at the head;
// a positive value is an interior fork at that IR logical time (the
// shared prefix below it freezes).
type ForkRequest struct {
	FigaroID string `json:"figaro_id"`
	AtMainLT uint64 `json:"at_main_lt,omitempty"`
}

// ForkResponse returns the two fresh child ids. The parent freezes and
// keeps its id as a navigable (read-only) index node. OwnerNote, when set,
// announces that an interior <id>:<LT> resolved to an owning ancestor (a
// parent trunk, a loadout, or the genesis root) and what was branched there.
type ForkResponse struct {
	Parent       string `json:"parent"`
	Continuation string `json:"continuation"`
	Alternative  string `json:"alternative"`
	OwnerNote    string `json:"owner_note,omitempty"`
}

// PromoteRequest climbs a conversation trunk up Levels stump-bounded levels,
// relabeling the canonical trunk path so it absorbs its parent trunk's run.
type PromoteRequest struct {
	FigaroID string `json:"figaro_id"`
	Levels   int    `json:"levels,omitempty"`
}

// PromoteResponse reports how many levels the trunk actually climbed. AtStump
// is true when it could not climb at all (the trunk is rooted at a loadout).
type PromoteResponse struct {
	FigaroID string `json:"figaro_id"`
	Climbed  int    `json:"climbed"`
	AtStump  bool   `json:"at_stump,omitempty"`
}

// Endpoint describes how to connect to a figaro.
type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
}

type KillRequest struct {
	FigaroID  string `json:"figaro_id"`
	Recursive bool   `json:"recursive,omitempty"` // also remove the trunk's live branches
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

// ListRequest options. IDsOnly skips the per-aria chalkboard + forest fills
// (mantra, cwd, loadout hash, vector) — much cheaper when the caller only needs
// the ids (e.g. shell completion). Global also includes the ceremonial anchors
// (the null genesis trunk + every versioned loadout) with Kind/Parent set, for
// the `ls -g` hierarchy and the `--json` escape hatch.
type ListRequest struct {
	IDsOnly bool `json:"ids_only,omitempty"`
	Global  bool `json:"global,omitempty"`
}

type ListResponse struct {
	Figaros []FigaroInfoResponse `json:"figaros"`
}

type BindRequest struct {
	PID      int    `json:"pid"`
	FigaroID string `json:"figaro_id"`
	AtMainLT uint64 `json:"at_main_lt,omitempty"` // pending fork-point; 0 = leaf
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
	AtMainLT uint64   `json:"at_main_lt,omitempty"` // pending fork-point bound to this pid
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
