package rpc

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

const (
	// Live-render wire (server -> client). MethodAriaFrame pushes aria.Page
	// values and MethodRead pulls the same shape for catch-up/paging. The UI
	// cursor is a turn id plus, for backward paging, a node ordinal; legacy
	// request field names still say LT. MethodTurnDone is separate control state.
	MethodAriaFrame = "figaro.aria" // push one aria.Page snapshot/delta
	MethodTurnDone  = "turn.done"   // turn ended; params report idle state

	// Requests.
	MethodQua       = "figaro.qua"
	MethodContext   = "figaro.context"
	MethodInterrupt = "figaro.interrupt"
	MethodSet       = "figaro.set"
	MethodForm      = "figaro.form"
	MethodQueued    = "figaro.queued"

	// MethodFormDelta pushes one committed form patch, in the shape a client
	// SENDS one. It rides the same fanout the aria's own frames do, so a
	// listener is an ordinary subscriber and there is no second transport.
	MethodFormDelta = "form.delta"

	// The queue mutators. Reading the queue stays on MethodQueued (it predates
	// them and its shape is unchanged); these two are the U and D of the CRUD,
	// while C is MethodQua, a queued message IS a submitted prompt, so there
	// is deliberately no second create path.
	// The study/cast family. Study/drop manage this aria's subscriptions
	// to UNBOUND forms (durable under system.studies); cast is the
	// casting-call: serialize in the aria's actor loop, ensure the study,
	// cross-call the role's writer to point target-aria here.
	MethodStudy = "figaro.study"
	MethodDrop  = "figaro.drop"
	MethodCast  = "figaro.cast"

	MethodQueueUpdate = "figaro.queue.update"
	MethodQueueDelete = "figaro.queue.delete"

	// MethodRead pulls one aria.Page from a turn cursor (the catch-up half of
	// the same paginated shape MethodAriaFrame pushes), so a (re)connecting
	// client can rebuild and follow live frames on the same connection.
	MethodRead = "figaro.read"
)

// The angelus-side read methods. They take an aria id and are answered
// from the store when no agent is live, so a dormant aria is readable
// without waking it: reading was the last thing that pinned an agent in
// memory. The per-aria equivalents above stay exactly as they are: when an
// agent IS live it holds in-flight state the store does not have, and
// these delegate to it.
const (
	MethodAriaPage    = "aria.page"    // one aria.Page window of sealed history
	MethodAriaContext = "aria.context" // fig IR plus render metrics
	MethodAriaForm    = "aria.form"    // the durable form snapshot
)

// MethodNeedsAgent reports whether a method requires a running turn loop.
//
// The false set is the read half: history, context and form are pure
// functions of the store, so an aria endpoint can answer them while the aria
// is dormant and nothing has to be woken. Everything else: prompting,
// interrupting, patching the board, touching the queue: either mutates
// durable state through the agent's serialization or needs in-flight state
// that only a live turn has, and must wake.
//
// Default is TRUE. A method added later and not classified here wakes the
// aria, which is the safe direction to be wrong in: a needless restore costs
// milliseconds, while serving a mutation from a stale read costs correctness.
func MethodNeedsAgent(method string) bool {
	switch method {
	case MethodRead, MethodContext, MethodForm:
		return false
	}
	return true
}

// Typed JSON-RPC error codes for figaro. The -32000..-32099 range
// is reserved by JSON-RPC 2.0 for application errors.
const (
	// ErrNoDefaultOutfit: config.toml has no default_outfit and the
	// request omitted one. Data: ErrorData{AvailableProviders}.
	ErrNoDefaultOutfit = -32010

	// ErrNoProvider: resolved outfit has no system.provider key.
	// Data: ErrorData{AvailableProviders, Outfit}.
	ErrNoProvider = -32011

	// ErrOutfitNotFound: named outfit is not on disk.
	// Data: ErrorData{Name, SearchPaths}.
	ErrOutfitNotFound = -32012
)

// ErrorData is the structured payload attached to typed JSON-RPC errors.
type ErrorData struct {
	AvailableProviders []string `json:"available_providers,omitempty"`
	Outfit             string   `json:"outfit,omitempty"`
	Name               string   `json:"name,omitempty"`
	SearchPaths        []string `json:"search_paths,omitempty"`

	// OutfitClosure is the layer graph an outfit was resolved through, sent
	// with ErrOutfitNotFound so the caller can show WHERE the gap is instead
	// of only naming it. Each node reports whether it was found on disk.
	OutfitClosure *OutfitLayer `json:"outfit_closure,omitempty"`
}

// OutfitLayer is one node of an outfit's layer closure. A node with no Name is
// the synthetic root that holds several requested outfits side by side.
type OutfitLayer struct {
	Name   string         `json:"name,omitempty"`
	Path   string         `json:"path,omitempty"`
	Found  bool           `json:"found"`
	Cycle  bool           `json:"cycle,omitempty"`
	Layers []*OutfitLayer `json:"layers,omitempty"`
}

const (
	MethodCreate = "figaro.create"
	// MethodFormCreate mints an UNBOUND FORM: fork the null root (or a
	// form) with a birth patch: kind "form", @-sigiled id, no agent,
	// no endpoint activation beyond the hub. The form half of the one
	// birth verb; figaro.create remains the figaro half.
	MethodFormCreate = "form.create"
	// MethodFormBind births a FIGARO from an unbound form: fork the form
	// (or the null root) with the caller's dressing plus runtime
	// fill-ins, stamp aria_id, stand the endpoint up, and construct NO
	// agent: the figaro is born dormant and wakes on first need, which
	// is where a missing provider fails (`bind null` mints fine and
	// errors at the first turn, by design).
	MethodFormBind    = "form.bind"
	MethodFork        = "figaro.fork"
	MethodPromote     = "figaro.promote"
	MethodImport      = "figaro.import"
	MethodNormalize   = "figaro.normalize"
	MethodGC          = "figaro.gc"
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

	// MethodOutfits answers what outfits exist and how one composes. The
	// outfits directory is the SERVER's state, so a client asks rather than
	// reading it: the daemon may not even share a filesystem with the caller.
	MethodOutfits = "angelus.outfits"
	// MethodOutfitReload flags the default form for recomputation on the
	// next fig new. There is deliberately no inverse: outfit files are
	// one-way sources of truth.
	MethodOutfitReload = "outfit.reload"

	// MethodConfigure patches the server's configuration. The first-run
	// wizard is a client, so it cannot write config.toml itself.
	MethodConfigure = "angelus.configure"

	MethodStatus       = "angelus.status"
	MethodSaveBindings = "angelus.save_bindings"
)

// QuaRequest is the prompt call with optional form input.
type QuaRequest struct {
	Text string     `json:"text"`
	Form *FormInput `json:"form,omitempty"`
}

// FormInput carries an optional state update: the client's passive view
// of state at send time, and the delta it means. An outfit is assembled into
// Patch by the client, so a prompt and the state it should be answered in are
// one call.
type FormInput struct {
	Context map[string]json.RawMessage `json:"context,omitempty"`
	Patch   *FormPatch                 `json:"patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

// FormDeltaSchema versions the ENVELOPE, not the patch. Version below is the
// patch's own durable index in the aria's form channel; this is the shape the
// two ends agreed on, and it moves when the shape does, a listener that reads
// a schema it does not know can say so instead of guessing.
const FormDeltaSchema = 1

// FormDelta is one committed transition, and it is deliberately the same shape
// in both directions: what a client sends as {patch, if_version} comes back as
// {patch, version}. A recording of a stream can be replayed at another aria.
type FormDelta struct {
	Schema  int       `json:"schema"`
	AriaID  string    `json:"aria_id,omitempty"`
	Version uint64    `json:"version"`
	Patch   FormPatch `json:"patch"`
	At      int64     `json:"at,omitempty"`
}

// FormPatch is the wire shape for a form delta. It is the internal
// patch: one type, so no boundary retypes it.
type FormPatch = message.Patch

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
// each name a disposition outright, a negated flag is how a caller ends up
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

// InterruptResponse reports the hangup. Queue is THE QUEUE AS OF THE HANGUP -
// one meaning, always populated, and Cleared says whether those messages were
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

// SetRequest applies a form patch directly.
type SetRequest struct {
	Patch FormPatch `json:"patch"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
	// IfVersion refuses the patch unless the board is still at this durable
	// version. Zero is unconditional. It exists for read-modify-write: editing
	// inside a value (an array element, a nested field) means reading it first,
	// and without this the write cannot tell that the value moved underneath.
	IfVersion uint64 `json:"if_version,omitempty"`
	// Assert makes a removal of a key that is not there a REFUSAL rather
	// than a no-op. A human or an agent deleting something absent has a
	// wrong model of the world; birth dressing does not, because `-D` means
	// "do not inherit this" about a closure that may never have held it.
	// Absent (false) keeps the older, forgiving rule.
	Assert bool `json:"assert,omitempty"`
	// Wait asks a LIVE aria to answer with the writer's verdict instead of
	// `queued`. It blocks until the next round boundary, which is as long as
	// a tool round, so it is opt-in and the default is unchanged. A dormant
	// aria is answered by the hub and was always synchronous, so this
	// changes nothing there.
	Wait bool `json:"wait,omitempty"`
}

// Set outcomes. Silence is not a legal answer to a command: a caller must be
// able to tell "I changed something" from "I changed nothing" from "I will
// find out later", and before these three all of them were OK with an empty
// list.
const (
	// OutcomeApplied: reduced, appended, fsynced. Version is the record.
	OutcomeApplied = "applied"
	// OutcomeUnchanged: legal, and it changed nothing. No record, no event,
	// and Version is where the board still stands.
	OutcomeUnchanged = "unchanged"
	// OutcomeQueued: accepted by a LIVE aria, which applies a set at the next
	// round boundary by design. The verdict (a stale IfVersion, an Assert
	// removal) is not known yet and Version is zero. Waiting for it here
	// would block the caller for the length of a tool round.
	OutcomeQueued = "queued"
)

type SetResponse struct {
	OK     bool     `json:"ok"`
	Set    []string `json:"set,omitempty"`
	Remove []string `json:"remove,omitempty"`
	// Outcome is which of the three above happened. Empty from a daemon that
	// predates them, which reads as the old ambiguous OK.
	Outcome string `json:"outcome,omitempty"`
	// Version is the durable version the board stands at after this call:
	// the new record for applied, the unmoved one for unchanged, zero for
	// queued. It is what a follower quotes back in IfVersion.
	Version uint64 `json:"version,omitempty"`
}

// FormResponse returns the agent's current snapshot and the durable
// version it stands at, which is what a conditional Set quotes back.
type FormResponse struct {
	Snapshot form.Snapshot `json:"snapshot"`
	Version  uint64        `json:"version,omitempty"`
}

// QueuedRequest asks for the messages this aria has accepted but not yet
// answered. IncludeCarriers opts in to the empty-text prompts that carry only
// a form patch: they are addressable by the CRUD surface and so must be
// listable, but they render as nothing, so the default stays exactly what it
// has always been: the prompts a human would recognise as queued.
type QueuedRequest struct {
	IncludeCarriers bool `json:"include_carriers,omitempty"`
}

// QueuedResponse carries the queued messages in FIFO order (oldest first).
//
// Epoch names the INBOX GENERATION these ids belong to. It is minted afresh
// every time an agent is constructed, a daemon restart, a dormant→attach -
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
	// IR. Visible, but no longer deletable: see QueueRejection.
	QueueStateCommitting QueueState = "committing"
)

// QueuedPrompt is one queued message. Text is the exact string submitted; a
// prompt with empty text is a pure form carrier and is only listed when
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
	// Form rides only on DRAINED payloads (the response to a clearing
	// hangup), so that what was drained can be persisted losslessly rather
	// than lost.
	Form *FormInput `json:"form,omitempty"`
}

// QueueOutcome is what happened to ONE requested mutation.
//
// There is deliberately no summary "ok" on either mutator response: reading
// the per-id outcome is the only way to learn anything, so it cannot be
// skipped and defaulted to success. A refusal is a legitimate decision by the
// agent: not a fault: so it travels as data, and the JSON-RPC error channel
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
// is non-empty: it is a compare-and-swap token, not decoration. All names no
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
	Mantra           string `json:"mantra"`                  // agent-maintained essence phrase (form "mantra")
	Cwd              string `json:"cwd"`                     // working directory (form "system.cwd")
	OutfitName       string `json:"outfit_name,omitempty"`   // form system.outfit_name
	OutfitVer        string `json:"outfit_ver,omitempty"`    // "live" if the stamped hash matches the current outfit, else its short hash
	BoundPIDs        []int  `json:"bound_pids"`

	// Fork-forest position (conversation nodes). Vector is the
	// child-index path (0, 0.0, 0.1, …); Trunk is the thread id that
	// flows down the continuation line; Parent is the aria branched from.
	// There is no "frozen" here: a fork leaves its target live under the
	// same id, so no aria is ever a read-only index node.
	Vector     []int  `json:"vector,omitempty"`
	Trunk      string `json:"trunk,omitempty"`
	Parent     string `json:"parent,omitempty"`
	BranchedLT uint64 `json:"branched_lt,omitempty"` // main-LT this trunk diverged at
	// Present is where the row is DRAWN: Parent unless a promote moved it.
	// Vector follows Present, so a listing draws one tree; Parent stays the
	// fork answer that `status` prints.
	Present string `json:"present,omitempty"`
	Kind    string `json:"kind,omitempty"` // "conversation" | "form" | "outfit" | "null" (set in global listings)

	// Unbound-form rows only: the form's "name" key, and: when the form is
	// a role (duck-typed by the key's presence): its target-aria.
	Name       string `json:"name,omitempty"`
	TargetAria string `json:"target_aria,omitempty"`
}

// CreateRequest names the outfit for a new aria. The system mints the
// aria id; callers cannot choose it.
// CreateRequest mints an aria from a patch. An empty patch means the
// configured default_outfit, which the angelus assembles; a patch that arrives
// is folded ON TOP of that default, so `-O mantra=x` adds rather than replaces.
type CreateRequest struct {
	Patch     *FormPatch `json:"patch,omitempty"`
	Ephemeral bool       `json:"ephemeral,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

type CreateResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// FormCreateRequest mints an unbound form. Parent "" forks the null root;
// a form id duplicates that form's state into a fresh @id. The patch is
// required, a fork that transforms nothing is a fork nobody can name.
type FormCreateRequest struct {
	Parent string     `json:"parent,omitempty"`
	Patch  *FormPatch `json:"patch"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

type FormCreateResponse struct {
	FormID   string   `json:"form_id"`
	Version  uint64   `json:"version"`
	Endpoint Endpoint `json:"endpoint"`
}

// FormBindRequest births a figaro from a form. Parent "" (or "null")
// forks the null root: the naked figaro. Patch is the optional -O
// dressing; unlike form.create it may be absent (runtime fill-ins make
// the birth patch nonempty).
type FormBindRequest struct {
	Parent string     `json:"parent,omitempty"`
	Patch  *FormPatch `json:"patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

type FormBindResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// StudyRequest subscribes (figaro.study) or unsubscribes (figaro.drop)
// the aria from an unbound form. Empty FormID on figaro.study lists.
type StudyRequest struct {
	FormID string `json:"form_id,omitempty"`
}

type StudyResponse struct {
	OK      bool     `json:"ok"`
	Studies []string `json:"studies"`
}

// CastRequest is one casting call. Exactly one of FormID / RolePatch:
// an existing role's id, or the patch a new role is BORN from (the
// server folds target-aria in, so nothing half-fails).
type CastRequest struct {
	FormID    string     `json:"form_id,omitempty"`
	RolePatch *FormPatch `json:"role_patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

// CastResponse reports the call's verdict, step by step, so a partial
// failure is a described state and never a mystery.
type CastResponse struct {
	RoleID  string `json:"role_id"`
	Studied bool   `json:"studied"` // newly studied by this call
	Patched bool   `json:"patched"` // target-aria points here
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
	// Patch dresses the ALTERNATIVE the moment it exists, before anything is
	// said to it: it lands on the child's form in the same call that
	// mints it, so a prompt sent next is answered with it in place.
	Patch *FormPatch `json:"patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

// ForkResponse returns the two fresh child ids. The parent freezes and
// keeps its id as a navigable (read-only) index node. OwnerNote, when set,
// announces that an interior <id>:<LT> resolved to an owning ancestor (a
// parent trunk, an outfit, or the genesis root) and what was branched there.
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

// ImportRequest restores an exported aria into this store as a NEW
// conversation. Nothing is grafted: the outfit is resolved or created by
// content, a fresh conversation is spawned under it, and Messages are appended
// through the ordinary path: so node ids, fork bases and LTs are the
// destination's own and can never collide with what is already there.
//
// WasID is the id the aria had where it came from. It is PROVENANCE, not a
// request: the destination mints its own, because a trunk id is unique per
// store and honouring the old one would be the first collision an import is
// built to avoid. It is echoed back so the caller can say what moved.
type ImportRequest struct {
	Outfit      string            `json:"outfit"`
	OutfitPatch message.Patch     `json:"outfit_patch,omitempty"`
	Form        message.Patch     `json:"form,omitempty"`
	Messages    []message.Message `json:"messages"`
	WasID       string            `json:"was_id,omitempty"`
	Mantra      string            `json:"mantra,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Model       string            `json:"model,omitempty"`
}

// ImportResponse names the aria that was created, and what it used to be.
type ImportResponse struct {
	FigaroID string `json:"figaro_id"`
	Outfit   string `json:"outfit"`
	Messages int    `json:"messages"`
	WasID    string `json:"was_id,omitempty"`
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

// GCRequest asks the angelus to collect outfit stumps nothing is using.
type GCRequest struct {
	// DryRun reports what would go without removing anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// GCStump is one outfit stump and its fate.
type GCStump struct {
	ID       string `json:"id"`
	Outfit   string `json:"outfit,omitempty"`
	Version  string `json:"version,omitempty"`
	Children int    `json:"children"`
	// Collected is true when it was removed, or: under DryRun: would be.
	Collected bool   `json:"collected,omitempty"`
	Err       string `json:"err,omitempty"`
}

// GCResponse lists every stump considered, in name order.
type GCResponse struct {
	Stumps    []GCStump `json:"stumps"`
	Collected int       `json:"collected"`
	DryRun    bool      `json:"dry_run,omitempty"`
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

// ListRequest options. IDsOnly skips the per-aria form + forest fills
// (mantra, cwd, outfit hash, vector): much cheaper when the caller only needs
// the ids (e.g. shell completion). Global also includes the ceremonial anchors
// (the null genesis trunk + every versioned outfit) with Kind/Parent set, for
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

// AriaIDRequest names an aria and nothing else: the whole request for the
// angelus-side context and form reads.
type AriaIDRequest struct {
	FigaroID string `json:"figaro_id"`
}

// AriaPageRequest is ReadRequest plus the aria it addresses. The cursor
// fields keep ReadRequest's names and meanings so a client can move between
// the per-aria socket and the angelus without reshaping its paging.
type AriaPageRequest struct {
	FigaroID   string `json:"figaro_id"`
	SinceLT    int    `json:"sinceLT,omitempty"`
	Before     int    `json:"before,omitempty"`
	BeforeNode int    `json:"before_node,omitempty"`
	Limit      int    `json:"limit,omitempty"`
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
	// FormDeltas is the record's form-state window, assembled HUB-SIDE
	// (internal/formdelta): the stamps and the patch logs live in the
	// store, and the client holds neither. Absent when the record's
	// windows were empty, and on every record of an ephemeral aria.
	FormDeltas map[string]livedoc.FormDelta `json:"form_deltas,omitempty"`
}

type AriaReadResponse struct {
	Entries  []AriaReadEntry `json:"entries"`
	Total    int             `json:"total"`               // total entries in the aria
	NextFrom uint64          `json:"next_from,omitempty"` // 0 when no more
}
