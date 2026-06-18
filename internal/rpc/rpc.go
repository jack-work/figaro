// Package rpc defines JSON-RPC 2.0 types shared between figaro and
// angelus sockets.
package rpc

import "github.com/jack-work/figaro/internal/message"

// Notification is a JSON-RPC 2.0 notification.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// --- Log frames (the stream respec wire vocabulary) ---
//
// What travels on the socket is the serialized Figaro IR. A sealed
// message is the bare message.Message (LogEntry); the open tail rides
// a thin envelope (OpenEntry / PatchEntry). See plan.md for the model.

// LogEntry carries a sealed, immutable message at its durable index.
// Params for MethodLogEntry; also the element type of ReadResponse.Entries.
type LogEntry struct {
	Index   uint64          `json:"index"`   // the durable LT
	Message message.Message `json:"message"` // bare IR, identical to disk
}

// OpenEntry is the current full state of the open (unsealed) tail.
// Params for MethodLogOpen; also ReadResponse.Open.
type OpenEntry struct {
	Index   uint64          `json:"index"`   // provisional; not durable until sealed
	Version uint64          `json:"version"` // per-open-message counter from 0; gap-detection sugar
	Open    bool            `json:"open"`    // always true on the wire
	Message message.Message `json:"message"` // current full IR state of the tail
}

// PatchEntry is a delta against the open message (delta mode only).
// Params for MethodLogPatch.
type PatchEntry struct {
	Index   uint64    `json:"index"`
	Version uint64    `json:"version"` // the version this patch PRODUCES
	From    uint64    `json:"from"`    // the version it applies to (== Version-1)
	Ops     []BlockOp `json:"ops"`
}

// BlockOp is one block-addressed edit to the open message's Content.
// Block is the 0-based ordinal in message.Message.Content.
type BlockOp struct {
	Op    string `json:"op"`    // open | append | replace | close
	Block uint64 `json:"block"` // index into Message.Content

	Text string `json:"text,omitempty"`         // op=append: text/thinking/tool_result body
	JSON string `json:"partial_json,omitempty"` // op=append: tool_invoke argument JSON

	// op=open / op=replace: the full block.
	Content *message.Content `json:"content,omitempty"`
}

// AbortEntry signals the open tail at Index was burned (never sealed).
// Params for MethodLogAbort.
type AbortEntry struct {
	Index  uint64 `json:"index"`
	Reason string `json:"reason"` // user_interrupt | fault | agent_exit
}

// DoneEntry signals the turn went idle. Params for MethodTurnDone.
type DoneEntry struct {
	Reason string `json:"reason"` // stop reason, or an error string
}


