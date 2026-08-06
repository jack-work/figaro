// Package message defines the provider-agnostic IR for figaro messages.
// Per-provider wire-format projections are cached alongside each message.
package message

import "encoding/json"

// Role identifies the participant in a conversation turn.
type Role string

const (
	// RoleInput and RoleOutput name the two voices. They are deliberately
	// under-specified: "user" was a lie the moment a subagent sent a message,
	// and figaro does not yet need to know WHO supplied input — only that it is
	// input. A finer distinction can be added later without another rename.
	//
	// These are figaro's INTERNAL vocabulary. Providers require literal
	// user/assistant on their own wire; every translator emits those literals
	// itself (e.g. nativeMessage{Role: "user"}), so the boundary is correct by
	// construction and this rename cannot reach a provider payload.
	RoleInput      Role = "input"
	RoleOutput     Role = "output"
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

// RoleFromWire maps a provider's wire vocabulary onto figaro's. Providers
// speak user/assistant; figaro speaks input/output. This is the ONLY place the
// mapping lives — both the decode boundary and UnmarshalJSON route through it,
// so the two directions cannot drift apart. Anything unrecognised passes
// through unchanged (tool_result, system, genesis are not voices).
func RoleFromWire(s string) Role {
	switch s {
	case "user":
		return RoleInput
	case "assistant":
		return RoleOutput
	}
	return Role(s)
}

// UnmarshalJSON accepts the pre-rename vocabulary so every aria written before
// this change keeps reading. It lives on the type rather than in the two
// projection sites because a decode path that forgets to normalise is a bug
// nobody would see until a turn rendered under the wrong voice — the same
// class of silent drift this refactor exists to delete.
//
// Writing is unconditional: MarshalJSON is the default, so new entries record
// input/output. The `ir` channel schema is bumped alongside, so an older binary
// refuses the store outright instead of misreading it.
func (r *Role) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*r = RoleFromWire(s)
	return nil
}

// IsCeremonial reports whether m is a structural/inherited marker rather than
// a conversational message: the root genesis sentinel, or the loadout-birth
// (a RoleInput message with no renderable content — it carries only the
// loadout's chalkboard stamp, inherited by every conversation in the shared
// prefix). These anchor the IR but are not turns, so the conversation's
// message count must not include them.
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

	// Sender attributes THIS block to whoever submitted it.
	//
	// A user message is not always one submission. Consecutive prompts drain
	// and fold into ONE message (mergePromptEvents), and those prompts may come
	// from different places: a human, a parent aria, a sibling three worktrees
	// away. Before this they arrived as one anonymous blob and arias genuinely
	// could not tell who was talking — there was nothing on the message to say.
	//
	// It lives on Content rather than on Message because Message.Content IS the
	// list of payloads, so one field makes each payload attributed without a
	// second parallel list for every consumer to learn and for the two to
	// disagree about. A multi-block submission (text plus an image) is a RUN of
	// Contents sharing a Sender; runs are self-describing, with no grouping
	// structure that can desync from the content it groups.
	//
	// Rendered form, not an id: "aria 76062b18" for an authenticated aria,
	// a bare label for an asserted one (see rpc.Attribution). Empty means
	// unknown, and every renderer draws NOTHING rather than "unknown" — a
	// blank attribution is noise on every message that never had one.
	Sender string `json:"sender,omitempty"`
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

	// Logical time: monotonic counter, unique per session. Populated on
	// read from the WAL frame index (the authoritative LT); omitempty so
	// it isn't persisted as a meaningless 0 in the payload.
	LogicalTime uint64 `json:"logical_time,omitempty"`

	// Steering marks an input message that arrived MID-TURN — a direction
	// meant to influence the train of thought already in flight, not a new
	// question. It is provenance, not content: the blocks are ordinary prose
	// and every provider encodes them exactly as before, so the model reads a
	// steer the same way it always did.
	//
	// It exists because the alternative — inferring steering from a prose
	// block co-occurring with a tool_result — cannot work when a steer
	// arrives while no tool is running, and that inference is precisely what
	// let a steer open its own turn and truncate the turn it meant to steer.
	// The drain that classifies is the only place that knows, so the drain
	// records it here.
	//
	// Legacy logs carry no flag; prose riding on a tool_result message is
	// still recognised as steering, so history written before this field
	// renders unchanged.
	Steering bool `json:"steering,omitempty"`

	// TurnID names the exchange this message belongs to: one user prompt
	// plus every assistant reply, tool round and steering interjection it
	// provoked. Monotonic per trunk and shared by every message of the
	// turn, so it is the coordinate `send`/`fork <trunk>:<turn>` address.
	// LT remains the storage substrate (positional, the cross-channel
	// foreign key); TurnID is what a human names. Messages preceding the
	// first prompt belong to no turn and carry 0. Absent on legacy entries,
	// where compose.StampTurnIDs derives it on read.
	TurnID uint64 `json:"turn_id,omitempty"`

	Timestamp int64 `json:"timestamp"`
}

func TextContent(text string) Content {
	return Content{Type: ContentProse, Text: text}
}

// SenderText is prose attributed to a submitter. An empty sender yields
// exactly TextContent, so an unattributed message serializes byte-identically
// to one written before Sender existed.
func SenderText(sender, text string) Content {
	return Content{Type: ContentProse, Text: text, Sender: sender}
}

func ImageContent(mimeType, data string) Content {
	return Content{Type: ContentImage, MimeType: mimeType, Data: data}
}

// ToolImageContent is an image a tool produced, riding on the tool_result
// tic alongside the text results and tagged with the call that produced it.
// The tag is what lets each encoder put the image where its provider wants
// it: Anthropic nests it inside the matching tool_result block, while the
// Responses encoder trails it in a following user message because a
// function_call_output there carries a plain string.
func ToolImageContent(toolCallID, toolName, mimeType, data string) Content {
	return Content{
		Type:       ContentImage,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		MimeType:   mimeType,
		Data:       data,
	}
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

// ToolImagesByCall indexes the tool-produced images on a message by the call
// that produced them — but only for calls that actually carry a tool_result
// block in that same message. An encoder that nests images inside their
// tool_result needs the restriction: an image naming a call with no result
// would be claimed by a block that is never rendered, and vanish, which is
// exactly the class of silent loss this indexing exists to prevent.
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

// MalformedArgsKey is the sole key of the arguments map figaro synthesizes for
// a tool call whose arguments DID NOT ARRIVE AS VALID JSON.
//
// A provider streams a tool call's arguments as a sequence of JSON fragments.
// When the concatenation is not parseable — a raw tab where an escape was
// owed, a string that never closes — the call cannot be executed, but the rest
// of the turn is unharmed: the thinking, the prose, and every other tool call
// are already in hand. So the block is QUARANTINED rather than mourned: the
// bytes that arrived are preserved verbatim under this key, which keeps the
// wire legal (a tool_use must replay, or its tool_result is orphaned) while
// telling everything downstream that the call must not run.
//
// The key is deliberately ugly and namespaced: it shares a map with argument
// names the model chose, and it must not collide with one.
const MalformedArgsKey = "__figaro_malformed_tool_input__"

// MalformedArgs is the arguments map for a quarantined tool call.
func MalformedArgs(raw string) map[string]interface{} {
	return map[string]interface{}{MalformedArgsKey: raw}
}

// MalformedArgsOf reports whether a tool invoke was quarantined, and returns
// the bytes that actually arrived.
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
