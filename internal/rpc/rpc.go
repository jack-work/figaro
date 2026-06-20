// Package rpc defines JSON-RPC 2.0 types shared between figaro and
// angelus sockets.
package rpc

import "github.com/jack-work/figaro/internal/livedoc"

// Notification is a JSON-RPC 2.0 notification.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// --- Live-render wire (typed node list + field splice) ---
//
// A conversation is an append-only sequence of units (one per
// conversational message: the user's prompt, then the agent's turn).
// Each unit is an ordered list of typed nodes (prose | tool). snapshot
// establishes a unit's full node list; node.open appends a node;
// node.patch splices a node's streamed string field (prose markdown or
// tool output); node.set updates a tool's scalar status; commit freezes
// the unit (the next snapshot/op is a new unit). There is no unit index —
// the server copy is authoritative and a faulted client reconnects and
// resnapshots.

// SnapshotEntry establishes the current live unit's full node list.
// Params for MethodLogSnapshot.
type SnapshotEntry struct {
	Role  string         `json:"role"`  // "user" | "assistant"
	Nodes []livedoc.Node `json:"nodes"` // the full node list for this unit
}

// NodeOpenEntry appends a node at Index. Params for MethodNodeOpen.
type NodeOpenEntry struct {
	Index int          `json:"index"`
	Node  livedoc.Node `json:"node"`
}

// NodePatchEntry is a single-region splice on a node's streamed string
// field ("markdown" | "output"). Byte offsets, rune-aligned. Params for
// MethodNodePatch.
type NodePatchEntry struct {
	Index int    `json:"index"`
	Field string `json:"field"`
	At    int    `json:"at"`
	Del   int    `json:"del"`
	Ins   string `json:"ins"`
}

// NodeSetEntry updates a tool node's scalar status. Params for
// MethodNodeSet.
type NodeSetEntry struct {
	Index  int    `json:"index"`
	Status string `json:"status"`
}

// CommitEntry freezes the current live unit. Params for MethodLogCommit.
type CommitEntry struct{}

// DoneEntry signals the turn went idle. Params for MethodTurnDone.
type DoneEntry struct {
	Reason string `json:"reason"` // stop reason, or an error string
}
