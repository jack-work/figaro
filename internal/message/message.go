// Package message defines the canonical IR (intermediate representation)
// for figaro's message types.
//
// These types are provider-agnostic. They serve as the common spec
// between providers, avoiding NxM translations. Each message carries
// opaque provider-specific "baggage" — the unaltered original
// representation from the originating provider. When sending back
// to the same provider, it can pull from baggage directly instead
// of re-converting from the IR.
package message

import "encoding/json"

// Role identifies the participant in a conversation turn.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
	RoleSystem     Role = "system" // compacted summary header
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

	// Tool result error flag
	IsError bool `json:"is_error,omitempty"`
}

// Usage tracks token consumption for a single assistant response.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Message is the canonical IR unit.
//
// It is the intermediate representation that all providers project
// to and from. The Baggage field carries the unaltered original
// response from the originating provider, keyed by provider name.
// This avoids re-conversion when sending back to the same provider.
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

	// Logical time: monotonic counter, one per tic.
	// Uniquely identifies this message in the session.
	LogicalTime uint64 `json:"logical_time"`

	// Timestamp in unix millis (wall clock, informational).
	Timestamp int64 `json:"timestamp"`

	// Baggage: provider name → unaltered original representation.
	// The originating provider stashes its native wire format here.
	// On re-send to the same provider, it pulls from baggage
	// instead of re-converting from the IR.
	Baggage map[string]json.RawMessage `json:"baggage,omitempty"`
}

// Block is the unit of conversation context: an optional compacted
// summary header followed by the ordered messages.
//
// This is what Store.Context() returns and what gets passed to the
// provider for conversion to its native format.
type Block struct {
	// Header is the compacted summary of earlier conversation.
	// Nil if no compaction has occurred.
	Header *Message

	// Messages is the ordered conversation from the first kept
	// message (or from the start if no compaction) to the leaf.
	Messages []Message
}

// --- convenience constructors ---

func TextContent(text string) Content {
	return Content{Type: ContentText, Text: text}
}

func ImageContent(mimeType, data string) Content {
	return Content{Type: ContentImage, MimeType: mimeType, Data: data}
}

func ToolCallContent(id, name string, args map[string]interface{}) Content {
	return Content{
		Type: ContentToolCall, ToolCallID: id,
		ToolName: name, Arguments: args,
	}
}

func NewToolResult(toolCallID, toolName string, content []Content, isError bool, lt uint64, ts int64) Message {
	for i := range content {
		content[i].IsError = isError
	}
	return Message{
		Role: RoleToolResult, Content: content,
		ToolCallID: toolCallID, ToolName: toolName,
		LogicalTime: lt, Timestamp: ts,
	}
}
