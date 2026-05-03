// Package provider defines the interface that LLM providers implement.
package provider

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
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

// ProjectionSummary is the synchronous output of Encode. Carries the
// per-message wire bytes the projection emitted, for cache persistence
// in the translation stream. The assembled assistant native bytes
// arrive separately as the return value of Send.
type ProjectionSummary struct {
	PerMessage  []json.RawMessage
	System      []json.RawMessage
	SystemFLT   uint64
	Fingerprint string
}

// Event is what the provider pushes to the Bus during Send. Each
// event lands on the translation stream's live tail.
type Event struct {
	Payload []json.RawMessage
}

// Bus is the publish surface the provider uses while streaming.
// Implemented by the agent's Inbox.
type Bus interface {
	Push(Event)
}

// Provider is the LLM provider interface.
//
// Encode + Decode are the translator surface (pure functions over IR
// and wire bytes). Send is pure transport: it takes pre-encoded bytes
// and ships them, pushing parsed native events to the bus and
// returning the assembled assistant native bytes when the stream
// closes.
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder configuration. Stored alongside
	// translation entries; mismatch invalidates them.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Encode produces the API request body bytes plus the per-message
	// projection summary. Pure function over conversation state.
	Encode(
		ctx context.Context,
		msgs []message.Message,
		snapshot chalkboard.Snapshot,
		priorTranslations causal.Slice[message.ProviderTranslation],
		tools []Tool,
		maxTokens int,
	) ([]byte, ProjectionSummary, error)

	// Decode reverses encoded native bytes back into IR.
	Decode(raw []json.RawMessage) ([]message.Message, error)

	// Send transports the pre-encoded body to the API, pushes parsed
	// native events into the bus as they arrive, and returns the
	// assembled assistant native bytes when the stream closes.
	// Pure transport — no encoding.
	Send(ctx context.Context, body []byte, bus Bus) ([]json.RawMessage, error)
}
