// Package provider defines the LLM provider interface.
package provider

import (
	"context"

	"github.com/jack-work/figaro/internal/chalkboard"
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

// Knobs are operational provider settings derived from the loadout's
// system.* chalkboard keys. The harness reads these to construct the
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
	PushFigaro(msg message.Message)
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

// SendInput is one turn's input.
type SendInput struct {
	AriaID    string
	FigLog    store.Log[message.Message]
	Snapshot  chalkboard.Snapshot
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
