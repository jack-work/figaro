package rpc

// THE READS: one name per verb, served on either door (see MethodRead).
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
)

type ContextResponse struct {
	Messages []interface{} `json:"messages"` // []message.Message, but interface{} for serialization flexibility
	Metrics  *aria.Metrics `json:"metrics,omitempty"`
}

// FormResponse returns the agent's current snapshot and the durable
// version it stands at, which is what a conditional Set quotes back.
// FormRequest asks for one form of a host. Continuo names WHICH: empty (or
// "state") is the identity segment, the host's own form; "queue" and "runtime"
// are the continuos. The address grammar is `<host>/<continuo>`, and `<host>`
// alone is shorthand for `<host>/state`.
type FormRequest struct {
	Continuo string `json:"continuo,omitempty"`
}

type FormResponse struct {
	Snapshot form.Snapshot `json:"snapshot"`
	Version  uint64        `json:"version,omitempty"`
	// Continuo echoes which form answered, so a client that asked for one it
	// does not get cannot mistake the board for it.
	Continuo string `json:"continuo,omitempty"`
}

// ReadRequest asks for one page of an aria at a coordinate. It is a keyset
// read: At names where to start, Backward says which way to walk, and the
// answer carries the coordinate to continue from (aria.Page.Next / .Prev), so
// a client pages an aria of any length without counting anything.
//
// At is a UI coordinate, not a logical time. The zero anchor means the end the
// direction starts from: the head of the aria going forward, its live tail
// going backward. A backward read excludes At itself, because the caller
// already holds it.
type ReadRequest struct {
	// FigaroID names the aria when the request arrives on the angelus door.
	// On an aria's own socket the connection already says which aria, and
	// this is empty.
	FigaroID string      `json:"figaro_id,omitempty"`
	At       aria.Anchor `json:"at,omitempty"`
	Backward bool        `json:"backward,omitempty"`
	// Limit is a byte budget. Zero asks for the server's configured page
	// size, which is what makes "start here, then follow Next" the whole of
	// the client's paging logic.
	Limit int `json:"limit,omitempty"`
}

// AriaIDRequest names an aria and nothing else: the whole request for the
// angelus-side context and form reads.
type AriaIDRequest struct {
	FigaroID string `json:"figaro_id"`
}

// IRRequest names the aria and the window of entries to return.
// From is inclusive; Limit==0 means "no upper bound". The angelus
// caps responses to a sensible upper bound regardless.
type IRRequest struct {
	FigaroID string `json:"figaro_id"`
	From     uint64 `json:"from,omitempty"`
	Before   uint64 `json:"before,omitempty"` // keyset pagination: return entries with LT < Before
	Limit    int    `json:"limit,omitempty"`
}

// IREntry is one IR entry on the wire, with LT separated from
// the payload so clients can ignore the figaro-internal envelope.
type IREntry struct {
	LT      uint64          `json:"lt"`
	Payload json.RawMessage `json:"payload"`
	// FormDeltas is the record's form-state window, assembled HUB-SIDE
	// (internal/formdelta): the stamps and the patch logs live in the
	// store, and the client holds neither. Absent when the record's
	// windows were empty.
	FormDeltas map[string]livedoc.FormDelta `json:"form_deltas,omitempty"`
}

type IRResponse struct {
	Entries  []IREntry `json:"entries"`
	Total    int       `json:"total"`               // total entries in the aria
	NextFrom uint64    `json:"next_from,omitempty"` // 0 when no more
}
