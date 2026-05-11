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

// ToolUseStartParams fires when the assistant begins a tool_use block.
// Lets the CLI show a spinner before args finish streaming.
type ToolUseStartParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

// ToolUseDeltaParams carries partial JSON for a tool's input.
// Best-effort; may be dropped under back-pressure.
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
// Emitted before tool_start for multi-tool rounds. Single-tool
// rounds skip this.
type ToolBatchStartParams struct {
	Size  int                  `json:"size"`
	Tools []ToolBatchToolEntry `json:"tools"`
}

// ToolBatchToolEntry describes one tool in a batch start.
type ToolBatchToolEntry struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

// ToolBatchEndParams closes a batch.
type ToolBatchEndParams struct {
	Size int `json:"size"`
}


