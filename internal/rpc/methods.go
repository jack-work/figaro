package rpc

import "encoding/json"

// --- Figaro socket methods (agent ops) ---

const (
	// Notifications: figaro → subscriber (no response expected).
	MethodDelta          = "stream.delta"
	MethodToolOutput     = "stream.tool_output"
	MethodThinking       = "stream.thinking"
	MethodToolStart      = "stream.tool_start"
	MethodToolEnd        = "stream.tool_end"
	MethodToolBatchStart = "stream.tool_batch_start"
	MethodToolBatchEnd   = "stream.tool_batch_end"
	MethodMessage        = "stream.message"
	MethodDone           = "stream.done"
	MethodError          = "stream.error"

	// Requests: client → figaro (response expected).
	MethodQua          = "figaro.qua"
	MethodContext      = "figaro.context"
	MethodInterrupt    = "figaro.interrupt"
	MethodReloadConfig = "figaro.reload_config"
	MethodSet          = "figaro.set"
	MethodChalkboard   = "figaro.chalkboard"
	// figaro.subscribe is handled at the transport level (long-lived connection).
)

// --- Angelus socket methods (registry ops) ---

const (
	MethodCreate      = "figaro.create"
	MethodKill        = "figaro.kill"
	MethodList        = "figaro.list"
	MethodAttach      = "figaro.attach"
	MethodAngelusInfo = "angelus.info"

	MethodBind    = "pid.bind"
	MethodResolve = "pid.resolve"
	MethodUnbind  = "pid.unbind"

	MethodStatus       = "angelus.status"
	MethodSaveBindings = "angelus.save_bindings"
)

// --- Figaro socket: request/response types ---

// QuaRequest is the figaro.qua call — "Figaro, qua!" — the user's
// next move. Optional chalkboard input rides along for one-shot
// state mutations.
type QuaRequest struct {
	Text       string           `json:"text"`
	Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"`
}

// ChalkboardInput carries an optional state update from the client.
// The presence of Patch is the discriminator:
//
//   - Patch only: apply the patch directly to the persisted snapshot.
//   - Context only: server diffs Context against the persisted snapshot
//     and applies the resulting patch.
//   - Both: server uses Context as the client's expected base (drift
//     detection); applies Patch on top.
//
// Schema is open — keys are whatever the client populates. Absence of a
// key in Context means deletion (for the Context-only and both paths).
type ChalkboardInput struct {
	Context map[string]json.RawMessage `json:"context,omitempty"`
	Patch   *ChalkboardPatch           `json:"patch,omitempty"`
}

// ChalkboardPatch is the wire shape for a chalkboard delta. The Go type
// is duplicated here (rather than importing chalkboard.Patch) to keep
// the rpc package import-graph at the leaf of the dependency tree —
// lower-level packages don't depend on chalkboard. The chalkboard
// package converts to/from this shape internally.
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

// ReloadConfigRequest re-runs the Outfitter's bootstrap phase and
// writes the resulting scribe-managed keys (system.prompt,
// system.skills) into the chalkboard. With DryRun set, the server
// computes the diff and returns it without persisting.
type ReloadConfigRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

// ReloadConfigResponse describes the patch produced by reload_config.
// SetKeys / RemoveKeys list the keys that changed; Applied is true
// when the patch was written to the chalkboard (false on dry-run).
type ReloadConfigResponse struct {
	Applied    bool     `json:"applied"`
	SetKeys    []string `json:"set_keys,omitempty"`
	RemoveKeys []string `json:"remove_keys,omitempty"`
}

// SetRequest applies a chalkboard patch directly — no LLM
// round-trip. Used by the `figaro set` / `figaro unset` CLI to
// configure runtime knobs (e.g. system.cache_control).
type SetRequest struct {
	Patch ChalkboardPatch `json:"patch"`
}

type SetResponse struct {
	OK     bool     `json:"ok"`
	Set    []string `json:"set,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// ChalkboardResponse returns the agent's current snapshot. Sorted is
// up to the caller — keys come back as the underlying map iteration
// provides them.
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

// --- Angelus socket: request/response types ---

// CreateRequest names the loadout that seeds the new aria's chalkboard.
// Lookup is `<configDir>/loadouts/<Loadout>.toml`, falling back to
// `<configDir>/providers/<Loadout>/config.toml`. Empty defaults to
// "config". Patch overlays the resolved loadout — used for runtime
// overrides like a one-shot model.
//
// ID is optional. Empty (the default) → angelus generates an 8-char
// UUID prefix. Non-empty → angelus uses the supplied id verbatim;
// must match `[A-Za-z0-9_-]{1,64}` and must not collide with a live
// figaro or an aria already on disk. Conflict returns an error.
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

// Endpoint describes how to connect to a figaro. Matches transport.Endpoint.
// Duplicated here so the RPC types don't import the transport package
// (keeps the wire format self-describing for non-Go clients).
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

// AttachRequest restores a dormant aria into the live registry without
// binding any pid. Used by `figaro aria <id> -- ...` to address a
// named aria without disturbing the shell's binding. No-op if the aria
// is already live; errors if the id is unknown to the backend.
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
