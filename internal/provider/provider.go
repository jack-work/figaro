// Package provider defines the LLM provider interface.
package provider

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"context_window"`
	MaxTokens     int    `json:"max_tokens"`
}

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// Knobs are operational provider settings derived from the outfit's
// system.* form keys. The harness reads these to construct the
// provider; the agent never sees them (no rendering template).
type Knobs struct {
	Model            string
	MaxTokens        int
	ReminderRenderer string // "tag" (default) or "tool"
	UseOfficialSDK   bool
}

// Bus is the sink for per-turn provider output. The figaro side folds
// these calls into the open tail message and emits log.* frames; the
// provider vocabulary is unchanged by the wire respec.
type Bus interface {
	PushDelta(content message.Content)
	PushFigaro(msg message.Message, cache ...AssistantCache)
	// PushToolInvokeStart fires when the assistant begins a tool_use
	// block — the model starts *authoring* an invocation. The figaro
	// side opens a tool_invoke block on the open assistant message.
	PushToolInvokeStart(toolCallID, toolName string)
	// PushToolInvokeDelta carries partial input JSON. Best-effort.
	PushToolInvokeDelta(toolCallID, partialJSON string)
	// PushToolReady fires when a tool_use block's input JSON is fully
	// decoded — typically at content_block_stop. The harness may dispatch
	// the tool immediately, before PushFigaro / message_stop arrives.
	//
	// The content must be a ContentToolInvoke with ToolCallID, ToolName,
	// and Arguments populated. Providers that don't support per-block
	// dispatch may omit calls to this method; the harness falls back to
	// dispatching from PushFigaro's assembled message.
	PushToolReady(call message.Content)
	// PushMessageEnd fires at message_stop, before PushFigaro. Under the
	// log.* model the figaro side ignores it (the stop reason rides the
	// sealed message), but providers still call it.
	PushMessageEnd(stopReason string)
}

// AssistantCache is the exact input-ready provider-native payload paired with
// a canonical assistant candidate. The actor commits both before acknowledging
// PushFigaro.
type AssistantCache struct {
	Namespace   string
	Payload     []json.RawMessage
	Fingerprint string
}

// Form is the per-LT transition accessor. Form patches no
// longer ride inline on IR messages; they live in a reducible channel keyed
// by IR logical time. PatchesAt returns the transitions to render on the
// message at lt — the encoder folds them into that message's wire bytes
// exactly as it did the inline patches, so per-LT caching stays sound
// (a message's bytes depend only on state up to that message). Live state for the
// system prefix still arrives via SendInput.Snapshot, which is rebuilt
// each turn and never cached per-LT.
// Form supplies the board patches that landed before a turn, so the
// projection can render a `set` inline where it happened.
//
// PatchesBetween takes form VERSIONS, not IR LTs. The board is
// unkeyed -- a patch is written with no reference to the timeline -- and
// the association runs the other way: each IR entry records how far the
// board had advanced when it was written. The projection hands over the
// previous entry's mark and this one's, and gets the patches that landed
// between them: (after, upTo].
//
// It is deliberately ABSOLUTE rather than a cursor. A cursor has to be
// driven exactly once, in order, from the beginning -- and the projection
// does no such thing: it warm-starts at previous.Entries and walks only the
// untranslated suffix. A fresh cursor pointed at that suffix replayed the
// WHOLE board onto the first new message, and the encoder baked it into the
// per-LT cache, so every provider round-trip permanently re-sent all of the
// aria's state. An absolute range cannot express that bug.
type Form interface {
	PatchesBetween(after, upTo uint64) []message.Patch
}

// SendInput is one turn's input.
type SendInput struct {
	AriaID    string
	FigLog    store.Log[message.Message]
	Snapshot  form.Snapshot
	Form      Form // inline transitions; nil = none (ephemeral)
	Tools     []Tool
	MaxTokens int
}

// Provider is the LLM provider interface.
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder config.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Send drives one turn end-to-end.
	Send(ctx context.Context, in SendInput, bus Bus) error
}

// ContextLimitProvider optionally reports the selected model's effective
// prompt-context cap from already-cached provider metadata. Implementations
// must not perform network I/O here because callers use it on live UI paths.
type ContextLimitProvider interface {
	ContextLimit(model string, snapshot form.Snapshot) int
}

// ContextLimitOverrideKey is the form key a user pins a context window
// with. It overrides whatever the provider would otherwise report.
const ContextLimitOverrideKey = "system.max_context_tokens"

// ContextLimitOverride reads the user's pinned context window off the
// form. One implementation so every provider spells the override the
// same way.
func ContextLimitOverride(snapshot form.Snapshot) (int, bool) {
	raw, ok := snapshot.Get(ContextLimitOverrideKey)
	if !ok {
		return 0, false
	}
	var limit int
	if json.Unmarshal(raw, &limit) != nil || limit <= 0 {
		return 0, false
	}
	return limit, true
}
