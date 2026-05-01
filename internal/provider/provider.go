// Package provider defines the interface that LLM providers implement.
package provider

import (
	"context"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// ModelInfo describes a model available from a provider.
type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"context_window"`
	MaxTokens     int    `json:"max_tokens"`
}

// Tool describes a tool the model can call.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"` // JSON Schema
}

// StreamEvent is a single chunk from a streaming response.
type StreamEvent struct {
	Delta       string
	ContentType message.ContentType
	BlockDone   bool
	Done        bool
	Message     *message.Message
	Err         error
}

// Provider is the LLM provider interface.
type Provider interface {
	// Name returns the provider identifier (e.g. "anthropic").
	Name() string

	// Fingerprint is a short opaque hash describing this provider's
	// current encoder configuration — anything that, if changed,
	// would alter the wire bytes produced for the same figaro IR.
	// Used by the translation cache: a stored entry whose
	// fingerprint does not match the provider's current value is
	// treated as stale and regenerated. Empty fingerprints are
	// allowed for providers that have nothing to hash; they always
	// match (no invalidation).
	Fingerprint() string

	// Models returns the list of models available from this provider.
	// Implementations may call the provider's API or return a static list.
	Models(ctx context.Context) ([]ModelInfo, error)

	// SetModel changes the model used for subsequent Send calls.
	SetModel(model string)

	// OpenAccumulator returns a per-stream native accumulator. The
	// streaming Send loop drives the accumulator alongside the
	// figaro accumulator that builds the IR Message; at stream end
	// the accumulator's Finalize is called with the figaro Message
	// and returns the message's ProviderTranslation entry.
	//
	// Today's typical implementation: a stub that ignores the live
	// stream (OnEvent is a no-op) and at Finalize projects the
	// figaro Message into wire form. Future SDK-backed
	// implementations may accumulate native shape natively from the
	// raw event stream and bypass the figaro round-trip.
	OpenAccumulator() NativeAccumulator

	// Send streams a conversation to the provider and returns response
	// events.
	//
	// The chalkboard snapshot carries per-aria state the provider may
	// consult — most notably system.prompt (the assembled credo) which
	// the provider injects into its native system block. Per-message
	// Patches still travel on each Message and are rendered inline as
	// system reminders per the provider's own configuration. Pass nil
	// for ephemeral arias that have no chalkboard.
	Send(ctx context.Context, block *message.Block, snapshot chalkboard.Snapshot, tools []Tool, maxTokens int) (<-chan StreamEvent, error)
}

// NativeAccumulator is the per-stream side of the provider that
// builds up the wire-format projection of one assistant response.
// Created at SSE open via Provider.OpenAccumulator, discarded after
// Finalize. Single-goroutine; no concurrent calls.
//
// Today the figaro accumulator inside the provider's HTTP layer
// builds the IR Message from raw events; this accumulator is a
// passenger that may either track raw events itself (future SDK
// integration) or ignore them and lean on the IR at Finalize time
// (today's stub).
type NativeAccumulator interface {
	// Finalize is called when the assistant message is complete.
	// Returns the per-provider translation entry to attach to the
	// figaro Message: a sequence of wire messages plus the
	// fingerprint of the provider's encoder configuration at write
	// time. The accumulator may consult its own state, the figaro
	// Message, or both.
	Finalize(figaroFinal message.Message) message.ProviderTranslation
}
