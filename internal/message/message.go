// Package message defines the canonical message types for figaro.
//
// These types are provider-agnostic. Each message may carry opaque
// provider-specific "baggage" — cached representations of the message
// in a provider's native format. Providers project to/from these types
// via the provider.Provider interface, and may stash their native
// representation in the baggage map to avoid redundant serialization
// on subsequent turns.
package message

import "encoding/json"

// Role identifies the participant in a conversation turn.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

// StopReason indicates why the assistant stopped generating.
type StopReason string

const (
	StopEnd     StopReason = "stop"
	StopLength  StopReason = "length"
	StopToolUse StopReason = "tool_use"
	StopError   StopReason = "error"
	StopAborted StopReason = "aborted"
)

// ContentType tags a content block.
type ContentType string

const (
	ContentText     ContentType = "text"
	ContentImage    ContentType = "image"
	ContentThinking ContentType = "thinking"
	ContentToolCall ContentType = "tool_call"
)

// Content is a single block within a message.
type Content struct {
	Type ContentType `json:"type"`

	// Text content (type == "text" or "thinking")
	Text string `json:"text,omitempty"`

	// Image content (type == "image")
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"` // base64

	// Tool call (type == "tool_call")
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`

	// Tool result (on messages with Role == RoleToolResult)
	IsError bool `json:"is_error,omitempty"`
}

// Usage tracks token consumption for a single assistant response.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Message is the canonical unit of conversation in figaro.
type Message struct {
	Role    Role      `json:"role"`
	Content []Content `json:"content"`

	// Assistant-only metadata
	Model      string     `json:"model,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Tool result metadata (role == "tool_result")
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// Timestamp in unix millis
	Timestamp int64 `json:"timestamp"`

	// Baggage: provider name -> opaque JSON object.
	// Each provider may cache its native message representation here.
	// Persisted to disk; ignored by providers that don't own the key.
	Baggage map[string]json.RawMessage `json:"baggage,omitempty"`
}

// TextContent is a convenience constructor.
func TextContent(text string) Content {
	return Content{Type: ContentText, Text: text}
}

// ToolCallContent is a convenience constructor.
func ToolCallContent(id, name string, args map[string]interface{}) Content {
	return Content{
		Type:       ContentToolCall,
		ToolCallID: id,
		ToolName:   name,
		Arguments:  args,
	}
}

// ToolResultMessage is a convenience constructor for a tool result message.
func ToolResultMessage(toolCallID, toolName string, content []Content, isError bool, ts int64) Message {
	for i := range content {
		content[i].IsError = isError
	}
	return Message{
		Role:       RoleToolResult,
		Content:    content,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Timestamp:  ts,
	}
}
