package rpc

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

const (
	// Live-render wire (server -> client). MethodAriaFrame pushes aria.Page
	// values and MethodRead pulls the same shape for catch-up/paging. The UI
	// cursor is a turn id plus, for backward paging, a node ordinal; legacy
	// request field names still say LT. MethodTurnDone is separate control state.
	MethodAriaFrame = "figaro.aria" // push one aria.Page snapshot/delta
	MethodTurnDone  = "turn.done"   // turn ended; params report idle state

	// Requests.
	MethodQua        = "figaro.qua"
	MethodContext    = "figaro.context"
	MethodInterrupt  = "figaro.interrupt"
	MethodSet        = "figaro.set"
	MethodLoadout    = "figaro.loadout"
	MethodChalkboard = "figaro.chalkboard"
	MethodQueued     = "figaro.queued"

	// The queue mutators. Reading the queue stays on MethodQueued (it predates
	// them and its shape is unchanged); these two are the U and D of the CRUD,
	// while C is MethodQua — a queued message IS a submitted prompt, so there
	// is deliberately no second create path.
	MethodQueueUpdate = "figaro.queue.update"
	MethodQueueDelete = "figaro.queue.delete"

	// MethodRead pulls one aria.Page from a turn cursor (the catch-up half of
	// the same paginated shape MethodAriaFrame pushes), so a (re)connecting
	// client can rebuild and follow live frames on the same connection.
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
	MethodNormalize   = "figaro.normalize"
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
	// Cursor is the newest materialized TURN id when the prompt is accepted.
	// The client reads from this idempotent resume point and then follows pushes
	// through the turn.done that reports whether the agent is idle.
	Cursor int `json:"cursor"`
	// Active reports whether a turn was already in flight when the prompt was
	// accepted (it queued/steers rather than starting fresh). The client uses
	// this to open the transcript pager immediately on the last page rather
	// than trying to render an already-in-progress turn inline.
	Active bool `json:"active,omitempty"`
}

// QueueDisposition says what a hangup does with the messages the aria has
// accepted but not yet answered. It is an explicit enum rather than a boolean
// because the two CLI verbs that carry it (`hup` keeps, `cut` discards) must
// each name a disposition outright — a negated flag is how a caller ends up
// discarding a queue it meant to keep.
type QueueDisposition string

const (
	// QueueKeep leaves the queue in place. It is the zero value, so a client
	// that predates this field gets exactly the old behaviour.
	QueueKeep QueueDisposition = "keep"
	// QueueClear drains the queue and returns what it drained.
	QueueClear QueueDisposition = "clear"
)

// InterruptRequest asks the aria to stop the turn in flight, and says what to
// do with anything queued behind it.
type InterruptRequest struct {
	Queue QueueDisposition `json:"queue,omitempty"` // "" == QueueKeep
}

// InterruptResponse reports the hangup. Queue is THE QUEUE AS OF THE HANGUP —
// one meaning, always populated — and Cleared says whether those messages were
// removed or left to be answered. Epoch names the inbox generation the ids in
// Queue belong to (see QueueDeleteRequest).
type InterruptResponse struct {
	OK      bool           `json:"ok"`
	Cleared bool           `json:"cleared,omitempty"`
	Epoch   string         `json:"epoch,omitempty"`
	Queue   []QueuedPrompt `json:"queue,omitempty"`
}

type ContextRequest struct{}

type ContextResponse struct {
	Messages []interface{} `json:"messages"` // []message.Message, but interface{} for serialization flexibility
	Metrics  *aria.Metrics `json:"metrics,omitempty"`
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
	Snapshot chalkboard.Snapshot `json:"snapshot"`
}

// QueuedRequest asks for the messages this aria has accepted but not yet
// answered. IncludeCarriers opts in to the empty-text prompts that carry only
// a chalkboard patch: they are addressable by the CRUD surface and so must be
// listable, but they render as nothing, so the default stays exactly what it
// has always been — the prompts a human would recognise as queued.
type QueuedRequest struct {
	IncludeCarriers bool `json:"include_carriers,omitempty"`
}

// QueuedResponse carries the queued messages in FIFO order (oldest first).
//
// Epoch names the INBOX GENERATION these ids belong to. It is minted afresh
// every time an agent is constructed — a daemon restart, a dormant→attach —
// and ids restart with it, so an id is only meaningful when paired with the
// epoch it was read against. Mutators require it back (QueueDeleteRequest);
// that is what stops a stale id from deleting a different message that happens
// to hold that number now.
type QueuedResponse struct {
	Epoch   string         `json:"epoch"`
	Prompts []QueuedPrompt `json:"prompts"`
}

// QueueState is where a message sits in its short life.
type QueueState string

const (
	// QueueStateQueued: in the inbox, deletable.
	QueueStateQueued QueueState = "queued"
	// QueueStateCommitting: lifted by the drain loop and on its way into the
	// IR. Visible, but no longer deletable — see QueueRejection.
	QueueStateCommitting QueueState = "committing"
)

// QueuedPrompt is one queued message. Text is the exact string submitted; a
// prompt with empty text is a pure chalkboard carrier and is only listed when
// the request asked for carriers.
type QueuedPrompt struct {
	ID    uint64     `json:"id"`
	Text  string     `json:"text"`
	State QueueState `json:"state,omitempty"`
	At    int64      `json:"at,omitempty"` // accepted-at, unix millis
	// Merged lists the ids folded INTO this message when an interrupt
	// coalesced a run of queued prompts, so a client holding one of those ids
	// can still find where it went.
	Merged []uint64 `json:"merged,omitempty"`
	// Chalkboard rides only on DRAINED payloads (the response to a clearing
	// hangup), so that what was drained can be persisted losslessly rather
	// than lost.
	Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"`
}

// QueueOutcome is what happened to ONE requested mutation.
//
// There is deliberately no summary "ok" on either mutator response: reading
// the per-id outcome is the only way to learn anything, so it cannot be
// skipped and defaulted to success. A refusal is a legitimate decision by the
// agent — not a fault — so it travels as data, and the JSON-RPC error channel
// stays reserved for transport and malformed requests.
type QueueOutcome string

const (
	QueueDeleted  QueueOutcome = "deleted"
	QueueUpdated  QueueOutcome = "updated"
	QueueRejected QueueOutcome = "rejected"
)

// QueueRejection is the closed set of reasons a mutation was refused.
type QueueRejection string

const (
	// RejectCommitting: the drain loop lifted it out of the queue as the
	// request arrived. It is becoming a message right now.
	RejectCommitting QueueRejection = "committing"
	// RejectCommitted: already appended to the IR by the running turn. This is
	// the honest answer to "delete the in-flight one": a legitimate ask, and a
	// legitimate refusal.
	RejectCommitted QueueRejection = "committed"
	// RejectMerged: an interrupt folded it into another queued message. Into
	// names the survivor, so the caller can retarget rather than guess.
	RejectMerged QueueRejection = "merged"
	// RejectStale: the epoch belongs to a previous inbox generation, so the id
	// cannot be resolved safely. Nothing was mutated.
	RejectStale QueueRejection = "stale"
	// RejectUnknown: never seen in this generation, or long since answered.
	RejectUnknown QueueRejection = "unknown"
	// RejectClosed: the inbox is shut (the aria is stopping).
	RejectClosed QueueRejection = "closed"
)

// QueueResult is one requested id's fate. Reason is set exactly when Outcome
// is QueueRejected; Detail is prose for a human and is never parsed.
type QueueResult struct {
	ID      uint64         `json:"id"`
	Outcome QueueOutcome   `json:"outcome"`
	Reason  QueueRejection `json:"reason,omitempty"`
	Detail  string         `json:"detail,omitempty"`
	Into    uint64         `json:"into,omitempty"` // RejectMerged: the surviving id
}

// QueueDeleteRequest asks the aria to drop queued messages.
//
// Epoch is the generation IDs were read against and is REQUIRED whenever IDs
// is non-empty — it is a compare-and-swap token, not decoration. All names no
// id at all ("whatever is queued now"), so it needs no epoch.
type QueueDeleteRequest struct {
	Epoch string   `json:"epoch,omitempty"`
	IDs   []uint64 `json:"ids,omitempty"`
	All   bool     `json:"all,omitempty"`
}

// QueueDeleteResponse carries one result per requested id, in request order.
// A stale or all-form request that resolves to nothing still reports a result
// (with ID 0 for the all-form), so an empty Results for a non-empty request is
// a protocol violation rather than a silent success.
type QueueDeleteResponse struct {
	Epoch   string        `json:"epoch"`
	Results []QueueResult `json:"results"`
}

// QueueUpdateRequest replaces the text of one queued message.
type QueueUpdateRequest struct {
	Epoch string `json:"epoch"`
	ID    uint64 `json:"id"`
	Text  string `json:"text"`
}

type QueueUpdateResponse struct {
	Epoch  string      `json:"epoch"`
	Result QueueResult `json:"result"`
}

// ReadRequest is the turn-shaped aria.Page request. SinceLT is a legacy JSON
// name: its value is the forward TURN cursor (0 = beginning). Before>0 switches
// to a backward keyset read from the (Before, BeforeNode) UI coordinate. That
// exact node is excluded because the caller already holds it; preserving the
// node offset keeps a clipped turn's head reachable. Limit is a byte budget.
type ReadRequest struct {
	SinceLT    int `json:"sinceLT,omitempty"`
	Before     int `json:"before,omitempty"`
	BeforeNode int `json:"before_node,omitempty"`
	Limit      int `json:"limit,omitempty"`
}

type FigaroInfoResponse struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	MessageCount     int    `json:"message_count"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`       // cumulative cache-hit tokens
	CacheWriteTokens int    `json:"cache_write_tokens"`      // cumulative cache-write tokens
	ContextTokens    int    `json:"context_tokens"`          // estimated next-turn input size
	ContextLimit     int    `json:"context_limit,omitempty"` // effective prompt cap when known
	ContextExact     bool   `json:"context_exact"`           // true if from Usage watermark
	CreatedAt        int64  `json:"created_at"`              // unix millis
	LastActive       int64  `json:"last_active"`             // unix millis
	Mantra           string `json:"mantra"`                  // agent-maintained essence phrase (chalkboard "mantra")
	Cwd              string `json:"cwd"`                     // working directory (chalkboard "system.cwd")
	LoadoutName      string `json:"loadout_name,omitempty"`  // chalkboard system.loadout_name
	LoadoutVer       string `json:"loadout_ver,omitempty"`   // "live" if the stamped hash matches the current loadout, else its short hash
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

// ForkRequest branches a conversation. With neither coordinate set it forks
// at the head.
type ForkRequest struct {
	FigaroID string `json:"figaro_id"`
	// AtTurn is the turn to REPLACE, the coordinate a human names
	// (`fig fork <id>:12`) and the one `fig show` prints. Turn N's fork
	// point is the LT that ENDS turn N-1, so the branch retains everything
	// through the previous exchange and the new prompt becomes turn N.
	//
	// The server owns that translation. It used to be done client-side,
	// which meant every caller read the aria's whole message list to
	// convert -- and one of them then sent the CONVERTED LT in this field,
	// a number the server read back as a turn. Since an LT is far larger
	// than the turn count, `send <id>:<turn>` failed every time with "aria
	// has no turn N".
	AtTurn uint64 `json:"at_turn,omitempty"`
	// AtLT is the same request in the MODEL's coordinate: the IR logical
	// time to fork at, exactly (`fig fork <id>.42` -- the dot form). It is
	// for callers that already hold an LT, and for forking where no turn
	// boundary exists, which a turn cannot express.
	//
	// Two named fields rather than one plus a flag, because the bug above
	// was a uint64 in the wrong slot: with separate names a misplaced
	// coordinate is a stated error in the handler instead of a plausible
	// number that means something else. Setting both is refused.
	AtLT uint64 `json:"at_lt,omitempty"`
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

// NormalizeRequest forces deferred topology work to run now. It is the one
// blocking operation in the trunk surface: everything else is instant
// because this can be postponed.
type NormalizeRequest struct {
	// Segments also repacks partially filled segment files. Not yet
	// implemented; the server reports it rather than silently ignoring it.
	Segments bool `json:"segments,omitempty"`
}

// NormalizeResponse reports how many arias were made independent of the
// ancestors they are no longer presented under.
type NormalizeResponse struct {
	Detached    int  `json:"detached"`
	Unsupported bool `json:"unsupported,omitempty"`
}

// PromoteRequest climbs a conversation trunk up Levels stump-bounded levels,
// relabeling the canonical trunk path so it absorbs its parent trunk's run.
type PromoteRequest struct {
	FigaroID string `json:"figaro_id"`
	Levels   int    `json:"levels,omitempty"`
}

// PromoteResponse reports how many levels the aria actually climbed.
// AtStump is true when it could not climb at all: it is already at the top
// of its presentation hierarchy, or the server has no trunk capability and
// therefore no hierarchy to edit (Unsupported).
type PromoteResponse struct {
	FigaroID string `json:"figaro_id"`
	Climbed  int    `json:"climbed"`
	AtStump  bool   `json:"at_stump,omitempty"`
	// Unsupported reports a server built without the trunk capability.
	Unsupported bool `json:"unsupported,omitempty"`
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
	// Build is the daemon's VCS revision. A CLI speaking a different build
	// than the running daemon renders nothing at all — the wire changes
	// between builds — so the mismatch must be named, not suffered.
	Build string `json:"build,omitempty"`
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
	Before   uint64 `json:"before,omitempty"` // keyset pagination: return entries with LT < Before
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
