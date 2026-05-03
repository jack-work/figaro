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

// ProjectionSummary is the synchronous output of Send. Carries the
// wire bytes the projection actually emitted (for cache persistence)
// plus the assembled assistant native the consumer should condense
// the live tail into.
type ProjectionSummary struct {
	PerMessage  []json.RawMessage
	System      []json.RawMessage
	SystemFLT   uint64
	Fingerprint string
	// Assistant is the assembled native message bytes for this turn.
	// One element today; reserved for N:1 native:figaro in future.
	Assistant []json.RawMessage
}

// Event is what the provider pushes to the Bus during Send. Each
// event lands on the translation stream's live tail. The act loop
// condenses the tail into one durable entry on SendComplete.
type Event struct {
	Payload []json.RawMessage
}

// Bus is the single publish surface the provider uses while
// streaming. Implemented by the agent's Inbox.
type Bus interface {
	Push(Event)
}

// Provider is the LLM provider interface. Pure transport + codec —
// no IR construction. Send pushes native events into the supplied
// Bus; the act loop decodes them into IR via Decode.
type Provider interface {
	Name() string

	// Fingerprint hashes the provider's current encoder configuration.
	// Stored alongside translation entries; mismatch invalidates them.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Send encodes msgs into the API request, opens the HTTP stream,
	// and pushes parsed native events into target. Live entries for
	// in-flight chunks (deltas), one durable entry for the assembled
	// final native message (with stop_reason + usage embedded).
	// Returns the projection summary so the caller can persist it.
	// Blocks until the stream closes or ctx is cancelled.
	Send(
		ctx context.Context,
		msgs []message.Message,
		snapshot chalkboard.Snapshot,
		priorTranslations causal.Slice[message.ProviderTranslation],
		tools []Tool,
		maxTokens int,
		bus Bus,
	) (ProjectionSummary, error)

	// Decode is the reverse of the Send projection: native bytes to
	// IR. Used by the act loop to advance the figaro stream from a
	// translation entry the provider just landed.
	Decode(raw []json.RawMessage) ([]message.Message, error)
}
