// Package provider defines the interface that LLM providers implement.
//
// Each provider owns its own per-aria translation cache (stored on
// disk by the agent's Backend) and drives one turn end-to-end via
// Send: catch up cache → POST → stream deltas through the Bus →
// land the assembled assistant message in figStream + cache.
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

// Bus is the agent-side sink for the provider's per-turn output. The
// turnBus implements it: PushDelta drives UX streaming; PushFigaro
// signals that an assembled assistant message has landed in figStream
// (the provider has already written it). PushToolUseStart and
// PushToolUseDelta let the provider surface assistant tool-call
// activity in real time so the client can render a spinner / progress
// without waiting for the assistant message to finalize.
type Bus interface {
	PushDelta(content message.Content)
	PushFigaro(msg message.Message)
	// PushToolUseStart fires when the assistant begins emitting a
	// tool_use content block — args may not be known yet.
	PushToolUseStart(toolCallID, toolName string)
	// PushToolUseDelta carries one chunk of partial input JSON for an
	// in-progress tool_use block. Best-effort; may be dropped under
	// back-pressure.
	PushToolUseDelta(toolCallID, partialJSON string)
}

// SendInput is one turn's input. The provider catches up its own
// per-aria cache from FigStream, builds the request body, POSTs,
// streams deltas through the bus, and on EOF appends the assembled
// assistant message to FigStream + cache. AriaID identifies the
// per-aria cache slot — providers open and own that file end-to-end.
type SendInput struct {
	AriaID    string
	FigStream store.Stream[message.Message]
	Snapshot  chalkboard.Snapshot
	Tools     []Tool
	MaxTokens int
}

// Provider is the LLM provider interface.
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder config. Mismatch invalidates
	// cached translator entries.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Send drives a turn end-to-end. Returns when the SSE stream has
	// closed and condensation is finished. Encode / Decode / Assemble
	// are private to each implementation.
	Send(ctx context.Context, in SendInput, bus Bus) error
}
