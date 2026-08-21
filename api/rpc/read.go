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
type FormResponse struct {
	Snapshot form.Snapshot `json:"snapshot"`
	Version  uint64        `json:"version,omitempty"`
}

// ReadRequest is the turn-shaped aria.Page request. SinceLT is a legacy JSON
// name: its value is the forward TURN cursor (0 = beginning). Before>0 switches
// to a backward keyset read from the (Before, BeforeNode) UI coordinate. That
// exact node is excluded because the caller already holds it; preserving the
// node offset keeps a clipped turn's head reachable. Limit is a byte budget.
type ReadRequest struct {
	// FigaroID names the aria when the request arrives on the ANGELUS door.
	// On an aria's own socket the connection already says which aria, and
	// this is empty. One request type, two doors: the field is how a client
	// addresses a read it did not open a per-aria connection for.
	FigaroID   string `json:"figaro_id,omitempty"`
	SinceLT    int    `json:"sinceLT,omitempty"`
	Before     int    `json:"before,omitempty"`
	BeforeNode int    `json:"before_node,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	// Backward pages toward the head. With no anchor that is THE TAIL
	// PAGE -- the newest turns -- which a zero SinceLT cannot ask for: a
	// forward read from a zero anchor starts at the HEAD, and a reader
	// that meant "the last N" gets the first N and no error.
	Backward bool `json:"backward,omitempty"`
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
