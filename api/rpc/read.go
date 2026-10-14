package rpc

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
)

type ContextResponse struct {
	Messages []interface{} `json:"messages"` // []message.Message
	Metrics  *aria.Metrics `json:"metrics,omitempty"`
}

// FormRequest.Intrinsic: empty or "state" is the host's own form; "queue"
// and "runtime" are the intrinsic forms. `<host>` is shorthand for
// `<host>/state`.
type FormRequest struct {
	Intrinsic string `json:"intrinsic,omitempty"`
	Since     uint64 `json:"since,omitempty"`
}

type FormVersionedPatch struct {
	Version uint64    `json:"version"`
	Patch   FormPatch `json:"patch"`
}

type FormResponse struct {
	Snapshot form.Snapshot `json:"snapshot"`
	Version  uint64        `json:"version,omitempty"`
	// Intrinsic echoes which form answered.
	Intrinsic string               `json:"intrinsic,omitempty"`
	Delta     bool                 `json:"delta,omitempty"`
	Patches   []FormVersionedPatch `json:"patches,omitempty"`
}

// ReadRequest is a keyset read; the answer carries the coordinate to continue
// from. The zero At means the end the direction starts from. A backward read
// excludes At itself.
type ReadRequest struct {
	// FigaroID is empty on an aria's own socket.
	FigaroID string      `json:"figaro_id,omitempty"`
	At       aria.Anchor `json:"at,omitempty"`
	Backward bool        `json:"backward,omitempty"`
	// Limit is a byte budget; zero asks for the server's configured page size.
	Limit int `json:"limit,omitempty"`
	// Floor stops a backward read at this anchor, inclusive; More.Before says
	// there is more below it. Zero means no floor.
	Floor aria.Anchor `json:"floor,omitempty"`
}

type AriaIDRequest struct {
	FigaroID string `json:"figaro_id"`
}

// IRRequest: From is inclusive; Limit 0 means no bound, but the angelus caps
// a response at 1000 entries and reports the rest in NextFrom. One call is one
// page.
type IRRequest struct {
	FigaroID string `json:"figaro_id"`
	From     uint64 `json:"from,omitempty"`
	Before   uint64 `json:"before,omitempty"` // entries with LT < Before
	Limit    int    `json:"limit,omitempty"`
}

type IREntry struct {
	LT      uint64          `json:"lt"`
	Payload json.RawMessage `json:"payload"`
	// FormDeltas is assembled hub-side; absent when the record's windows
	// were empty.
	FormDeltas map[string]livedoc.FormDelta `json:"form_deltas,omitempty"`
}

type IRResponse struct {
	Entries  []IREntry `json:"entries"`
	Total    int       `json:"total"`
	NextFrom uint64    `json:"next_from,omitempty"` // 0 when no more
}

type ModelsWantRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ModelsWantResponse struct {
	OK bool `json:"ok"`
}
