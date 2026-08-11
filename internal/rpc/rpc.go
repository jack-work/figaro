// Package rpc defines JSON-RPC 2.0 types shared between figaro and
// angelus sockets.
//
// The live-render wire is aria.Page (see internal/livelog/aria), pushed via
// MethodAriaFrame and pulled for catch-up/paging via MethodRead. Snapshot nodes
// ride directly in TurnParts; only the newest mutable suffix uses NodeDelta.
package rpc

// Notification is a JSON-RPC 2.0 notification.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// DoneEntry signals that a turn ended. Idle reports whether queued work remains.
// Params for MethodTurnDone.
type DoneEntry struct {
	Reason string `json:"reason"` // stop reason, or an error string
	// Idle is true when the agent has no more queued work. A pointer so the
	// client can distinguish "absent" (a daemon predating this field: treat as
	// settled, the pre-steering behavior) from an explicit false (a turn that
	// ended with a steer still queued: keep waiting).
	Idle *bool `json:"idle,omitempty"`
}
