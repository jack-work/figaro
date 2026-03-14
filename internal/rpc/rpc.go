// Package rpc defines the JSON-RPC 2.0 notification types for figaro.
//
// These are written to stdout as newline-delimited JSON.
// The figaro process streams notifications as it works;
// a frontend process reads and renders them.
//
// Messages in the store are also IR messages with the same
// baggage structure. The stdout stream is a live view of what
// gets persisted.
package rpc

import "github.com/jack-work/figaro/internal/message"

// Notification is a JSON-RPC 2.0 notification (no id, no response).
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// --- Notification params ---

type DeltaParams struct {
	Text        string              `json:"text"`
	ContentType message.ContentType `json:"content_type"`
}

type ThinkingParams struct {
	Text string `json:"text"`
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
	SessionID string `json:"session_id"`
	FigaroID  string `json:"figaro_id,omitempty"`
	Reason    string `json:"reason"`
}

type ErrorParams struct {
	Message string `json:"message"`
}

// Method constants.
const (
	MethodDelta     = "stream.delta"
	MethodThinking  = "stream.thinking"
	MethodToolStart = "stream.tool_start"
	MethodToolEnd   = "stream.tool_end"
	MethodMessage   = "stream.message"
	MethodDone      = "stream.done"
	MethodError     = "stream.error"
)
