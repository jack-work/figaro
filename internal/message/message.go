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

	// RoleSystemInterrupt is a sentinel inserted when a turn left
	// unmatched tool_invoke blocks (interrupt, fault, agent exit). The
	// IR stays append-only; the translator is responsible for
	// emitting a provider-acceptable surrogate (e.g., a synthetic
	// tool_result block) into the wire stream.
	RoleSystemInterrupt Role = "system.interrupt"

	// RoleGenesis marks a node's birth message in the IR — written when a
	// fork node is created (null root, loadout node, conversation) so the
	// log is non-empty and forkable, and to anchor provenance. It is
	// filtered from provider rendering (it is structural, not a turn).
	RoleGenesis Role = "genesis"
)

// IsGenesis reports whether m is a structural birth message.
func IsGenesis(m Message) bool { return m.Role == RoleGenesis }

// IsCeremonial reports whether m is a structural/inherited marker rather than
// a conversational message: the root genesis sentinel, or the loadout-birth
// (a RoleUser message with no renderable content — it carries only the
// loadout's chalkboard stamp, inherited by every conversation in the shared
// prefix). These anchor the IR but are not turns, so the conversation's
// message count must not include them.
func IsCeremonial(m Message) bool {
	if m.Role == RoleGenesis {
		return true
	}
	if m.Role == RoleUser {
		for _, c := range m.Content {
			if c.Type == ContentProse && c.Text != "" {
				return false
			}
			if c.Type == ContentImage || c.Type == ContentToolResult {
				return false
			}
		}
		return true // empty user marker (loadout birth)
	}
	return false
}

// CountMessages is the SINGLE SOURCE OF TRUTH for a conversation's message
// count: the number of conversational (non-ceremonial) messages in an IR
// timeline. Every derivation (live FigaroInfo, the meta sidecar, the durable
// usage/meta snapshots) routes through this so the count is identical no
// matter where it is computed — and, because the figwal head is now a single
// deterministic leaf, it does not depend on fork head-selection order.
func CountMessages(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		if !IsCeremonial(m) {
			n++
		}
	}
	return n
}

// InterruptReason classifies why a system.interrupt sentinel was
// inserted. Travels on each interrupt content block as Text-prefixed
// metadata; kept open-coded so unknown values pass through.
type InterruptReason string

const (
	InterruptFault         InterruptReason = "fault"
	InterruptUserInterrupt InterruptReason = "user_interrupt"
	InterruptAgentExit     InterruptReason = "agent_exit"
)

// StopReason indicates why the assistant stopped generating.
type StopReason string

const (
	StopEnd        StopReason = "stop"
	StopLength     StopReason = "length"
	StopToolInvoke StopReason = "tool_invoke"
	StopError      StopReason = "error"
	StopAborted    StopReason = "aborted"
)

// ContentType tags a content block.
type ContentType string

const (
	// ContentProse is an assistant/user markdown span. Named to match the
	// UI IR's "prose" node (livedoc.NodeProse) — figaro IR and UI IR are
	// converging on shared primitive names (prose / thinking / tool / image).
	ContentProse      ContentType = "prose"
	ContentImage      ContentType = "image"
	ContentThinking   ContentType = "thinking"
	ContentToolInvoke ContentType = "tool_invoke" // assistant emits these
	ContentToolResult ContentType = "tool_result" // user-role message carries these (one block per tool that completed)

	// ContentInterrupt blocks live on a RoleSystemInterrupt message,
	// one per dangling tool_call_id from the prior assistant turn.
	// ToolCallID names the unmatched call; Reason carries a short
	// machine-readable classification; Text carries a human-readable
	// description echoed into the synthetic wire surrogate.
	ContentInterrupt ContentType = "interrupt"
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

	// Reason populates ContentInterrupt blocks.
	Reason InterruptReason `json:"reason,omitempty"`
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

	// Patches are chalkboard mutations for this message.
	Patches []Patch `json:"patches,omitempty"`

	// Assistant-only metadata. (model/provider are NOT here — they are
	// chalkboard values: system.model / system.provider, derived on read.)
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Deprecated: tool result metadata moving to Content blocks.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// Logical time: monotonic counter, unique per session. Populated on
	// read from the WAL frame index (the authoritative LT); omitempty so
	// it isn't persisted as a meaningless 0 in the payload.
	LogicalTime uint64 `json:"logical_time,omitempty"`

	Timestamp int64 `json:"timestamp"`
}

func TextContent(text string) Content {
	return Content{Type: ContentProse, Text: text}
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

// InterruptContent constructs a single interrupt block naming one
// dangling tool_call_id.
func InterruptContent(toolCallID, toolName string, reason InterruptReason, text string) Content {
	return Content{
		Type:       ContentInterrupt,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Reason:     reason,
		Text:       text,
	}
}

// NewInterruptSentinel constructs a RoleSystemInterrupt message naming
// every tool_invoke from the provided assistant content blocks. Callers
// pass the tool_invoke blocks from the dangling assistant turn.
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

// IsInterruptSentinel reports whether m is a system.interrupt message.
func IsInterruptSentinel(m Message) bool { return m.Role == RoleSystemInterrupt }

// DanglingToolCallIDs returns the tool_call_ids named by the
// ContentInterrupt blocks in m. Empty for non-sentinel messages.
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
