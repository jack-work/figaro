// Package message defines the provider-agnostic IR for figaro messages.
// Per-provider wire-format projections are cached alongside each message.
package message

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
	ContentText       ContentType = "text"
	ContentImage      ContentType = "image"
	ContentThinking   ContentType = "thinking"
	ContentToolCall   ContentType = "tool_call"   // assistant emits these
	ContentToolResult ContentType = "tool_result" // user-role tic carries these (one block per tool that completed)
)

// Content is a single block within a message. Type determines
// which fields are populated.
type Content struct {
	Type ContentType `json:"type"`


	Text string `json:"text,omitempty"`


	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"` // base64


	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`


	IsError bool `json:"is_error,omitempty"`
}

// Usage tracks token consumption for a single assistant response.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Message is the canonical IR unit for conversation turns and
// state-only events. Per-provider wire projections are cached
// in translator streams keyed by LogicalTime.
type Message struct {
	Role    Role      `json:"role"`
	Content []Content `json:"content"`

	// Patches are chalkboard mutations for this tic.
	Patches []Patch `json:"patches,omitempty"`

	// Assistant-only metadata
	Model      string     `json:"model,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Deprecated: tool result metadata moving to Content blocks.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// Logical time: monotonic counter, unique per session.
	LogicalTime uint64 `json:"logical_time"`


	Timestamp int64 `json:"timestamp"`
}



func TextContent(text string) Content {
	return Content{Type: ContentText, Text: text}
}

func ImageContent(mimeType, data string) Content {
	return Content{Type: ContentImage, MimeType: mimeType, Data: data}
}

// ToolResultContent constructs a tool_result content block.
func ToolResultContent(toolCallID, toolName, text string, isErr bool) Content {
	return Content{
		Type:       ContentToolResult,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Text:       text,
		IsError:    isErr,
	}
}
