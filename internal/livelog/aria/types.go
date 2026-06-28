// Package aria implements figaro's single paginated read API.
//
// An AriaRead is one "aria read": a page of the conversation. The live stream
// pushes AriaReads as state changes — server-pushed pagination — so subscribing
// is semantically identical to repeatedly calling read(sinceLT). The cursor is a
// figaro LT (logical time): to catch up after a miss or a reconnect, a client
// reads from its last received LT.
//
// Empty sections are omitted to save bytes on noisy chats: an absent committed
// section means "no newly-closed messages", an absent live section means "no
// open-message change".
//
// Invariants (per stream connection):
//   - a given LT appears in the committed section at most once; seeing it twice
//     on one connection is a violation.
//   - a message appears in committed once and never again on that connection
//     (a reconnect with an earlier cursor may re-deliver it — that's a new
//     connection, governed by the cursor).
//   - a message may spend time in the live section (its nodes updating) before
//     it closes into committed.
package aria

// AriaRead is one page — returned by Read or pushed live; the two are equivalent.
type AriaRead struct {
	Committed []Committed `json:"committed,omitempty"`
	Live      *Live       `json:"live,omitempty"`
}

// Empty reports whether the page carries nothing (so it isn't sent).
func (r AriaRead) Empty() bool { return len(r.Committed) == 0 && r.Live == nil }

// Committed is a closed message, in one of two shapes:
//
//	full  — Role+Nodes present: the message's content (catch-up / first delivery)
//	patch — Closed=true, Nodes absent: the message at LT just closed; the client
//	        already streamed its content live, so only the close transition is
//	        signaled. A close-patch sorts before any newly-created full messages
//	        in the same page (it has the lower LT).
type Committed struct {
	LT     int    `json:"lt"`
	Closed bool   `json:"closed,omitempty"`
	Role   string `json:"role,omitempty"`
	Nodes  []Node `json:"nodes,omitempty"`
}

// Live is the currently-open message and the blocks that changed.
type Live struct {
	LT    int    `json:"lt"`
	Role  string `json:"role,omitempty"`
	Nodes []Node `json:"nodes"`
}

// Node is a block: a stable id, a monotonic version, and its representation.
// Each block loosely binds to one UI element, which the id+version address.
//
// Phase 1 carries the FULL representation on every update (the whole text). Phase
// 2 will allow a patch on the prior version (Base + a splice Delta) — the same
// item, just compressed. Either way a Node is a representation of the node.
type Node struct {
	ID      string `json:"id"`
	Version int    `json:"v"`

	Type     string                 `json:"type"` // prose | thinking | tool
	Markdown string                 `json:"markdown,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Args     map[string]interface{} `json:"args,omitempty"`
	Status   string                 `json:"status,omitempty"`
	Output   string                 `json:"output,omitempty"`
}

// Message is a closed (immutable) message, identified by its figaro LT.
type Message struct {
	LT    int
	Role  string
	Nodes []Node
}
