// Package rpc defines the JSON-RPC 2.0 message types for figaro.
//
// These are written to stdout as newline-delimited JSON.
// The figaro process streams notifications as it works;
// a frontend process reads them and renders.
//
// This is NOT a request/response protocol — the figaro process
// is a forked child of the shell. It reads args, does work,
// streams notifications to stdout, and exits.
//
// Messages in the store are also jsonrpc2 — the same envelope
// wraps both persisted conversation entries and streamed output.
// This means the store is a log of jsonrpc2 messages, and the
// stdout stream is a live view of that same log.
package rpc

import "github.com/jack-work/figaro/internal/message"

// Notification is a JSON-RPC 2.0 notification (no id, no response).
// Written to stdout as newline-delimited JSON.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"` // always "2.0"
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// --- Stream notifications (figaro → frontend) ---

// DeltaParams is emitted as the LLM streams text.
type DeltaParams struct {
	Text        string              `json:"text"`
	ContentType message.ContentType `json:"content_type"`
}

// ThinkingParams is emitted for thinking/reasoning tokens.
type ThinkingParams struct {
	Text string `json:"text"`
}

// ToolStartParams is emitted when a tool begins execution.
type ToolStartParams struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

// ToolEndParams is emitted when a tool finishes.
type ToolEndParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Result     string `json:"result"`
	IsError    bool   `json:"is_error,omitempty"`
}

// MessageParams wraps a full message (user, assistant, tool_result).
// Emitted when a complete message is appended to the store.
type MessageParams struct {
	EntryID string          `json:"entry_id"`
	Message message.Message `json:"message"`
}

// DoneParams signals the agent loop has finished.
type DoneParams struct {
	SessionID string `json:"session_id"`
	FigaroID  string `json:"figaro_id"`
	Reason    string `json:"reason"` // "stop", "error", "aborted"
}

// ErrorParams signals a fatal error.
type ErrorParams struct {
	Message string `json:"message"`
}

// Notification method constants.
const (
	MethodDelta     = "stream.delta"
	MethodThinking  = "stream.thinking"
	MethodToolStart = "stream.tool_start"
	MethodToolEnd   = "stream.tool_end"
	MethodMessage   = "stream.message"
	MethodDone      = "stream.done"
	MethodError     = "stream.error"
)
