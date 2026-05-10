// Package rpc defines the JSON-RPC 2.0 types shared across figaro components.
//
// Two protocols use these types:
//
//  1. Figaro socket (agent ops): prompt, context, subscribe, info
//     + stream notifications (delta, message, tool_start, etc.)
//
//  2. Angelus socket (registry ops): create, kill, list, info, bind, resolve, unbind, status
//
// All communication across process boundaries is JSON-RPC 2.0.
// This package defines the shared types so that any client in any
// language can implement the protocol.
package rpc

import "github.com/jack-work/figaro/internal/message"

// --- Figaro socket: notification params (streamed to subscribers) ---

// Notification is a JSON-RPC 2.0 notification (no id, no response).
// Used internally by the agent to emit events on the in-process channel.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type DeltaParams struct {
	Text        string              `json:"text"`
	ContentType message.ContentType `json:"content_type"`
}

type ThinkingParams struct {
	Text string `json:"text"`
}

// ToolUseStartParams fires the moment the assistant begins emitting a
// tool_use block — well before the full args have been streamed and
// before the assistant message lands. Lets the CLI render a spinner
// for the upcoming tool without waiting for content_block_stop.
// MethodToolStart still fires later (with the parsed args) once the
// message is finalized; the two events bracket "tool announced" vs
// "tool executing".
type ToolUseStartParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

// ToolUseDeltaParams carries a chunk of partial JSON for the tool's
// input as the model streams it. Best-effort — chunks may be dropped
// under back-pressure. Useful for showing progress (bytes streamed)
// or, if the client is willing to parse partial JSON, previewing the
// args mid-stream.
type ToolUseDeltaParams struct {
	ToolCallID  string `json:"tool_call_id"`
	PartialJSON string `json:"partial_json"`
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

// ToolBatchStartParams brackets a parallel tool dispatch round.
// The agent emits this *before* any tool_start notifications when a
// round contains more than one tool call. The CLI uses this to
// switch into batch render mode: pre-allocate N status rows,
// suppress per-chunk streaming, summarize on completion. For
// single-tool rounds the agent skips this notification entirely
// (Size would be 1, redundant), preserving the live-streaming UX.
type ToolBatchStartParams struct {
	Size  int                  `json:"size"`
	Tools []ToolBatchToolEntry `json:"tools"`
}

// ToolBatchToolEntry is a single tool entry in a batch start. It
// matches the fields the CLI needs to render the pending row
// (name + a short detail), without forcing the client to wait for
// each tool_start to arrive.
type ToolBatchToolEntry struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

// ToolBatchEndParams closes a batch. Emitted after every tool_end
// in the round has been delivered.
type ToolBatchEndParams struct {
	Size int `json:"size"`
}


