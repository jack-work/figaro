package rpc

// THE TURN: asking, steering, interrupting, and patching a board.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/message"
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
	OK bool `json:"ok"`
	// Stopped says whether there WAS a turn to stop. Without it the client
	// cannot tell "I interrupted the model" from "there was nothing running",
	// and both came back as OK: so the pager guessed from its own turn state,
	// which is a frame or two behind the daemon and was wrong exactly when it
	// mattered -- a turn in flight, and the pager saying "nothing to
	// interrupt".
	Stopped bool           `json:"stopped,omitempty"`
	Cleared bool           `json:"cleared,omitempty"`
	Epoch   string         `json:"epoch,omitempty"`
	Queue   []QueuedPrompt `json:"queue,omitempty"`
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
