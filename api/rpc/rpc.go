// Package rpc defines the JSON-RPC 2.0 wire types.
package rpc

type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// DoneEntry is the params of MethodTurnDone.
type DoneEntry struct {
	Reason string `json:"reason"` // stop reason, or an error string
	// Idle is a pointer: absent (an older daemon) reads as settled, explicit
	// false as a steer still queued.
	Idle *bool `json:"idle,omitempty"`
}
