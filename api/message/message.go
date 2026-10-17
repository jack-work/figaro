// Package message defines the provider-agnostic IR for figaro messages.
package message

import "encoding/json"

type Role string

const (
	// RoleInput and RoleOutput are deliberately under-specified: input is
	// input regardless of who supplied it.
	RoleInput      Role = "input"
	RoleOutput     Role = "output"
	RoleToolResult Role = "tool_result"
	RoleSystem     Role = "system" // compacted summary header

	// RoleSystemInterrupt marks a turn that left unmatched tool_invoke blocks;
	// the translator emits the provider-acceptable surrogate.
	RoleSystemInterrupt Role = "system.interrupt"

	// RoleGenesis is a node's birth message: structural, filtered from
	// provider rendering.
	RoleGenesis Role = "genesis"
)

// RoleFromWire maps user/assistant onto input/output; the decode boundary and
// UnmarshalJSON both route through it. Unrecognised values pass through.
func RoleFromWire(s string) Role {
	switch s {
	case "user":
		return RoleInput
	case "assistant":
		return RoleOutput
	}
	return Role(s)
}

// UnmarshalJSON accepts the pre-rename user/assistant vocabulary.
func (r *Role) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*r = RoleFromWire(s)
	return nil
}

// IsCeremonial reports whether m is a structural marker (genesis, or an
// input message with no renderable content: the outfit birth) rather than a
// turn. Ceremonial messages are excluded from the message count.
func IsCeremonial(m Message) bool {
	if m.Role == RoleGenesis {
		return true
	}
	if m.Role == RoleInput {
		for _, c := range m.Content {
			if c.Type == ContentProse && c.Text != "" {
				return false
			}
			if c.Type == ContentImage || c.Type == ContentToolResult {
				return false
			}
		}
		return true
	}
	return false
}

// CountMessages is the single definition of a conversation's message count;
// every derivation routes through it.
func CountMessages(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		if !IsCeremonial(m) {
			n++
		}
	}
	return n
}

// InterruptReason is open-coded so unknown values pass through.
type InterruptReason string

const (
	InterruptFault         InterruptReason = "fault"
	InterruptUserInterrupt InterruptReason = "user_interrupt"
	InterruptAgentExit     InterruptReason = "agent_exit"
)

type StopReason string

const (
	StopEnd        StopReason = "stop"
	StopLength     StopReason = "length"
	StopToolInvoke StopReason = "tool_invoke"
	StopError      StopReason = "error"
	StopAborted    StopReason = "aborted"
)

type ContentType string

const (
	ContentProse      ContentType = "prose"
	ContentImage      ContentType = "image"
	ContentThinking   ContentType = "thinking"
	ContentToolInvoke ContentType = "tool_invoke"
	ContentToolResult ContentType = "tool_result" // one block per completed tool, on an input-role message

	// ContentInterrupt blocks live on a RoleSystemInterrupt message, one per
	// dangling tool_call_id.
	ContentInterrupt ContentType = "interrupt"
)

// Content is one block; Type determines which fields are populated.
type Content struct {
	Type ContentType `json:"type"`

	Text string `json:"text,omitempty"`

	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"` // base64

	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`

	IsError bool `json:"is_error,omitempty"`

	Reason InterruptReason `json:"reason,omitempty"`

	Sender string `json:"sender,omitempty"`
}

type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// StudyMark states a study/drop transition in the IR.
type StudyMark struct {
	FormID string `json:"form_id"`
	Began  bool   `json:"began"`
}

type Message struct {
	Role    Role      `json:"role"`
	Content []Content `json:"content"`

	Patches []Patch `json:"patches,omitempty"`

	// Study records that this aria began or stopped observing a form here.
	Study *StudyMark `json:"study,omitempty"`

	// StudyPatches are the observed forms' transitions for this message,
	// keyed by form id. Populated at projection time, never persisted: the
	// studied forms' channels are the durable truth.
	StudyPatches map[string][]Patch `json:"-"`

	// StudyAt is the version each observed form stood at when this message
	// was stamped: the upper bound of the window StudyPatches folds.
	StudyAt map[string]uint64 `json:"-"`

	// Assistant-only. Model and provider are form values, not fields here.
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// LogicalTime is populated on read from the WAL frame index; omitempty
	// keeps a meaningless 0 out of the payload.
	LogicalTime uint64 `json:"logical_time,omitempty"`

	// Steering marks an input message that arrived mid-turn. Provenance only:
	// providers encode the blocks exactly as ordinary prose.
	Steering bool `json:"steering,omitempty"`

	// TurnID is shared by every message of a turn and monotonic per trunk;
	// messages preceding the first prompt carry 0. Always serialized: with
	// omitempty "no turn" and "predates turn ids" were the same bytes.
	TurnID uint64 `json:"turn_id"`

	Timestamp int64 `json:"timestamp"`

	Principal string `json:"principal,omitempty"`
}

func TextContent(text string) Content {
	return Content{Type: ContentProse, Text: text}
}

// SenderText with an empty sender serializes byte-identically to TextContent.
func SenderText(sender, text string) Content {
	return Content{Type: ContentProse, Text: text, Sender: sender}
}

func ImageContent(mimeType, data string) Content {
	return Content{Type: ContentImage, MimeType: mimeType, Data: data}
}

// ToolImageContent is an image a tool produced, tagged with the call so each
// encoder can place it where its provider wants it.
func ToolImageContent(toolCallID, toolName, mimeType, data string) Content {
	return Content{
		Type:       ContentImage,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		MimeType:   mimeType,
		Data:       data,
	}
}

func ToolResultContent(toolCallID, toolName, text string, isErr bool) Content {
	return Content{
		Type:       ContentToolResult,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Text:       text,
		IsError:    isErr,
	}
}

func InterruptContent(toolCallID, toolName string, reason InterruptReason, text string) Content {
	return Content{
		Type:       ContentInterrupt,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Reason:     reason,
		Text:       text,
	}
}

// NewInterruptSentinel names every tool_invoke among calls; other blocks are
// ignored.
func NewInterruptSentinel(reason InterruptReason, text string, calls []Content) Message {
	blocks := make([]Content, 0, len(calls))
	for _, c := range calls {
		if c.Type != ContentToolInvoke {
			continue
		}
		blocks = append(blocks, InterruptContent(c.ToolCallID, c.ToolName, reason, text))
	}
	return Message{
		Role:    RoleSystemInterrupt,
		Content: blocks,
	}
}

func IsInterruptSentinel(m Message) bool { return m.Role == RoleSystemInterrupt }

// DanglingToolCallIDs is empty for non-sentinel messages.
func DanglingToolCallIDs(m Message) []string {
	if !IsInterruptSentinel(m) {
		return nil
	}
	ids := make([]string, 0, len(m.Content))
	for _, c := range m.Content {
		if c.Type == ContentInterrupt && c.ToolCallID != "" {
			ids = append(ids, c.ToolCallID)
		}
	}
	return ids
}

// ToolImagesByCall indexes tool-produced images by call, only for calls that
// carry a tool_result in the same message: an encoder nesting images inside
// their tool_result would otherwise drop an image whose block never renders.
func ToolImagesByCall(content []Content) map[string][]Content {
	results := make(map[string]bool)
	for _, c := range content {
		if c.Type == ContentToolResult && c.ToolCallID != "" {
			results[c.ToolCallID] = true
		}
	}
	if len(results) == 0 {
		return nil
	}
	var out map[string][]Content
	for _, c := range content {
		if c.Type != ContentImage || c.Data == "" || !results[c.ToolCallID] {
			continue
		}
		if out == nil {
			out = make(map[string][]Content)
		}
		out[c.ToolCallID] = append(out[c.ToolCallID], c)
	}
	return out
}

// MalformedArgsKey is the sole key of the arguments map synthesized for a tool
// call whose arguments did not arrive as valid JSON.
const MalformedArgsKey = "__figaro_malformed_tool_input__"

func MalformedArgs(raw string) map[string]interface{} {
	return map[string]interface{}{MalformedArgsKey: raw}
}

// InvalidJSONResult is the tool_result content for a quarantined call, in the
// shape Anthropic publishes for fine-grained tool streaming.
func InvalidJSONResult(raw string) string {
	b, err := json.Marshal(map[string]string{"INVALID_JSON": raw})
	if err != nil {
		return "INVALID_JSON: the tool input could not be parsed or re-encoded"
	}
	return string(b)
}

// MalformedArgsOf reports whether a tool invoke was quarantined, and returns
// the bytes that arrived.
func MalformedArgsOf(c Content) (string, bool) {
	if c.Type != ContentToolInvoke || len(c.Arguments) != 1 {
		return "", false
	}
	raw, ok := c.Arguments[MalformedArgsKey]
	if !ok {
		return "", false
	}
	s, _ := raw.(string)
	return s, true
}
