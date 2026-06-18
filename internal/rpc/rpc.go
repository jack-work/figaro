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

type DeltaParams struct {
	Text        string              `json:"text"`
	ContentType message.ContentType `json:"content_type"`
}

type ThinkingParams struct {
	Text string `json:"text"`
}

// ToolInvokeStartParams fires when the assistant begins a tool_use
// block (Anthropic vocabulary). On the figaro wire we call this an
// "invoke" to keep the lifecycle distinct from the harness
// `stream.tool_*` events (which describe execution, not authoring).
// Lets the CLI show a spinner before args finish streaming.
type ToolInvokeStartParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

// ToolInvokeDeltaParams carries partial JSON for a tool's input.
// Best-effort; may be dropped under back-pressure.
type ToolInvokeDeltaParams struct {
	ToolCallID  string `json:"tool_call_id"`
	PartialJSON string `json:"partial_json"`
}

// ToolInvokeReadyParams fires once a tool's input JSON is fully
// decoded — the model has finished authoring this invocation. The
// harness uses this internally to begin executing the tool
// speculatively; the CLI uses it as the signal that args are settled.
type ToolInvokeReadyParams struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

type ToolStartParams struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

type ToolEndParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Result     string `json:"result"`
	IsError    bool   `json:"is_error,omitempty"`
}

type MessageParams struct {
	LogicalTime uint64          `json:"logical_time"`
	Message     message.Message `json:"message"`
}

// MessageEndParams fires at message_stop, before MessageParams. It
// carries just the stop reason so the CLI can commit to rendering
// decisions (e.g. solo vs batch) without parsing the full assistant
// message. The full Message follows in MessageParams.
type MessageEndParams struct {
	StopReason string `json:"stop_reason"`
}

type DoneParams struct {
	Reason string `json:"reason"`
}

type ToolOutputParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Chunk      string `json:"chunk"`
}

type ErrorParams struct {
	Message string `json:"message"`
}

