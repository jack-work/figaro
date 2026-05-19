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

