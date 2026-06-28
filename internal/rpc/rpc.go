// Package rpc defines JSON-RPC 2.0 types shared between figaro and
// angelus sockets.
//
// The live-render wire is the aria read (see internal/livelog/aria): the
// conversation is delivered as AriaRead pages, pushed live via
// MethodAriaFrame and pulled for catch-up via MethodRead. There is no
// positional op stream — the page carries livedoc.Nodes directly.
package rpc

// Notification is a JSON-RPC 2.0 notification.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// DoneEntry signals the turn went idle. Params for MethodTurnDone.
type DoneEntry struct {
	Reason string `json:"reason"` // stop reason, or an error string
}
