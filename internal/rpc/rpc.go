// Package rpc defines JSON-RPC 2.0 types shared between figaro and
// angelus sockets.
package rpc

// Notification is a JSON-RPC 2.0 notification.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// --- Live-render wire (blob + splice) ---
//
// A conversation is an append-only sequence of units (one per
// conversational message: the user's prompt, then the agent's turn).
// Each unit is one markdown blob that mutates only by single-region
// splices. snapshot establishes a unit's full text; delta patches the
// current live unit; commit freezes it (the next snapshot/delta is a new
// unit). There is no unit index — the server copy is authoritative and a
// faulted client reconnects and resnapshots.

// SnapshotEntry establishes the current live unit's full markdown.
// Params for MethodLogSnapshot.
type SnapshotEntry struct {
	Role     string `json:"role"`     // "user" | "assistant"
	Markdown string `json:"markdown"` // the full blob for this unit
}

// DeltaEntry is a single-region splice against the current live unit's
// blob. Byte offsets, rune-aligned. Params for MethodLogDelta.
type DeltaEntry struct {
	At  int    `json:"at"`
	Del int    `json:"del"`
	Ins string `json:"ins"`
}

// CommitEntry freezes the current live unit. Params for MethodLogCommit.
type CommitEntry struct{}

// DoneEntry signals the turn went idle. Params for MethodTurnDone.
type DoneEntry struct {
	Reason string `json:"reason"` // stop reason, or an error string
}
