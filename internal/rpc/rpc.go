// Package rpc defines the JSON-RPC message types for figaro's
// CLI-to-daemon protocol.
//
// The daemon holds sessions in memory and streams responses.
// The CLI is a thin client that sends prompts and renders output.
//
// Transport: unix domain socket, newline-delimited JSON.
// Each message is a single JSON object followed by \n.
//
// Flow:
//
//	CLI                              DAEMON
//	 │                                 │
//	 ├─ PromptRequest ────────────────►│
//	 │                                 ├── (calls LLM, executes tools)
//	 │◄──────────────── StreamEvent ───┤  (repeated: deltas, tool status)
//	 │◄──────────────── StreamEvent ───┤
//	 │◄──────────────── StreamEvent ───┤  Done=true
//	 │                                 │
//	 ├─ SessionListRequest ──────────►│
//	 │◄──────────── SessionListResponse│
//	 │                                 │
package rpc

import "github.com/jack-work/figaro/internal/message"

// --- Envelope ---

// Request is the top-level client→daemon message.
type Request struct {
	ID     string      `json:"id"`               // correlates responses
	Method string      `json:"method"`           // "prompt", "session.list", etc.
	Params interface{} `json:"params,omitempty"`
}

// Response is a single daemon→client message.
// For streaming methods, multiple Responses share the same ID.
type Response struct {
	ID     string      `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  *Error      `json:"error,omitempty"`
	Stream bool        `json:"stream,omitempty"` // true if more responses follow
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- prompt ---

// PromptParams is the payload for method "prompt".
type PromptParams struct {
	// SessionID to continue, or empty for a new session.
	SessionID string `json:"session_id,omitempty"`

	// Text is the user's prompt.
	Text string `json:"text"`

	// Cwd is the working directory for tool execution.
	// Required when creating a new session.
	Cwd string `json:"cwd,omitempty"`

	// SystemPrompt overrides the default. Optional.
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// StreamChunk is a single streaming result for method "prompt".
// Sent as Response.Result with Response.Stream=true until Done.
type StreamChunk struct {
	// Type distinguishes what this chunk carries.
	Type StreamChunkType `json:"type"`

	// Delta: incremental text (type == "delta")
	Delta       string              `json:"delta,omitempty"`
	ContentType message.ContentType `json:"content_type,omitempty"`

	// ToolStart: a tool is about to execute (type == "tool_start")
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// ToolEnd: a tool finished (type == "tool_end")
	ToolResult string `json:"tool_result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`

	// Done: the agent loop finished (type == "done")
	// No more StreamChunks follow for this request ID.
	Done bool `json:"done,omitempty"`

	// Error during streaming (type == "error")
	ErrorMessage string `json:"error_message,omitempty"`

	// SessionID is included on the first and last chunk
	// so the CLI knows which session was used.
	SessionID string `json:"session_id,omitempty"`
}

type StreamChunkType string

const (
	ChunkDelta     StreamChunkType = "delta"
	ChunkToolStart StreamChunkType = "tool_start"
	ChunkToolEnd   StreamChunkType = "tool_end"
	ChunkDone      StreamChunkType = "done"
	ChunkError     StreamChunkType = "error"
)

// --- session.list ---

// SessionListParams is the payload for method "session.list".
type SessionListParams struct {
	Cwd string `json:"cwd,omitempty"` // filter by cwd, or empty for all
}

// SessionInfo is a summary of a session.
type SessionInfo struct {
	ID           string `json:"id"`
	Cwd          string `json:"cwd"`
	CreatedAt    int64  `json:"created_at"`    // unix millis
	ModifiedAt   int64  `json:"modified_at"`   // unix millis
	MessageCount int    `json:"message_count"`
	FirstMessage string `json:"first_message"` // truncated
}

// --- session.context ---

// SessionContextParams is the payload for method "session.context".
// Returns the current LLM context for a session (for debugging/inspection).
type SessionContextParams struct {
	SessionID string `json:"session_id"`
}

// SessionContextResult is the response for "session.context".
type SessionContextResult struct {
	Messages []message.Message `json:"messages"`
}

// --- session.branch ---

// SessionBranchParams is the payload for method "session.branch".
type SessionBranchParams struct {
	SessionID string `json:"session_id"`
	EntryID   string `json:"entry_id"` // fork point
}

// --- session.compact ---

// SessionCompactParams is the payload for method "session.compact".
type SessionCompactParams struct {
	SessionID          string `json:"session_id"`
	CustomInstructions string `json:"custom_instructions,omitempty"`
}
