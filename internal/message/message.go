// Package message defines the canonical IR (intermediate representation)
// for figaro's message types.
//
// These types are provider-agnostic. They serve as the common spec
// between providers, avoiding NxM translations. Each message carries
// per-provider "translation" entries — cached wire-format projections
// from the originating provider. When sending back to the same
// provider, it can pull from the cached translation directly instead
// of re-converting from the IR.
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

// Content is a single block within a message.
//
// The block's Type determines which other fields carry meaningful data:
//
//   - text      → Text
//   - thinking  → Text (the thinking content; signature is provider-side)
//   - image     → MimeType + Data
//   - tool_call → ToolCallID + ToolName + Arguments (assistant turn)
//   - tool_result → ToolCallID + Text (or Image fields) + IsError
//     (user-role tic; one block per tool that completed since the
//     last assistant turn)
type Content struct {
	Type ContentType `json:"type"`

	// Text content (type == "text", "thinking", or "tool_result"
	// when the result is text)
	Text string `json:"text,omitempty"`

	// Image content (type == "image", or "tool_result" when the
	// result is an image)
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"` // base64

	// Tool identifiers. Used by:
	//   - tool_call:   ToolCallID + ToolName (and Arguments)
	//   - tool_result: ToolCallID (matching the tool_call this answers)
	//                  ToolName is also populated for human-readable logs
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`

	// IsError is set on tool_result blocks when the tool execution
	// returned an error.
	IsError bool `json:"is_error,omitempty"`
}

// Usage tracks token consumption for a single assistant response.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Message is the canonical IR unit — both for conversational turns
// (a user message, a tic accumulating events between agent responses,
// or an assistant response) and for state-only timeline events
// (bootstrap and rehydrate, which are user-role Messages with only
// Patches and no Content). See plans/aria-storage/log-unification.md.
//
// All providers project to and from Message. Per-provider wire-format
// projections are cached in a parallel translation log
// (arias/{id}/translations/{provider}.jsonl), keyed by
// Message.LogicalTime — see internal/store/translog.go. On re-send
// to the same provider, the agent supplies the cached translation
// alongside the block; the provider falls back to fresh rendering
// on cache misses.
type Message struct {
	Role    Role      `json:"role"`
	Content []Content `json:"content"`

	// Patches are chalkboard mutations that arrived during the time
	// delta this Message represents. Populated for user-role
	// Messages whose tic carried state changes; nil otherwise.
	// State-only tics (bootstrap, rehydrate) carry only Patches
	// (no Content) and emit zero wire output but contribute to the
	// chalkboard snapshot the projection reads.
	Patches []Patch `json:"patches,omitempty"`

	// Assistant-only metadata
	Model      string     `json:"model,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Tool result metadata (role == "tool_result").
	//
	// Deprecated: scheduled to retire in a follow-up Stage A commit;
	// tool_result data moves to per-Content-block fields when the
	// ContentToolResult content type lands. New code should not rely
	// on these Message-level fields.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// Logical time: monotonic counter, one per tic.
	// Uniquely identifies this message in the session.
	LogicalTime uint64 `json:"logical_time"`

	// Timestamp in unix millis (wall clock, informational).
	Timestamp int64 `json:"timestamp"`

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

// ToolResultContent constructs a tool_result content block. Used by
// the agent loop to append a tool's result to the in-progress tic.
// Multiple tool_result blocks can coexist in one user-role Message
// (one per tool that completed since the last assistant turn).
//
// The result data goes into the Text field; for richer payloads
// (images, structured content) callers can use TextContent / ImageContent
// in a separate block — but Anthropic's wire shape expects a single
// text or image inside each tool_result, so this helper keeps it
// simple.
func ToolResultContent(toolCallID, toolName, text string, isErr bool) Content {
	return Content{
		Type:       ContentToolResult,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Text:       text,
		IsError:    isErr,
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
