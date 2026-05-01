// Package provider defines the interface that LLM providers implement.
package provider

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/internal/causal"
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

// ProjectionSummary is the synchronous output of a Send projection.
// Returned alongside the StreamEvent channel so the agent can persist
// the wire bytes that this Send actually emitted (Stage D.2f
// write-through translation persistence).
//
// All RawMessages are wire-format bytes WITHOUT cache_control
// markers — those are recomputed per-Send by the provider's
// markCacheBreakpoints (or equivalent) and persisting them with the
// markers attached would couple cached bytes to the message's
// position in the conversation, which moves between Sends.
type ProjectionSummary struct {
	// PerMessage holds wire bytes parallel-indexed with
	// block.Messages. PerMessage[i] is the wire form of the figaro
	// Message at block.Messages[i] — typically one wire message per
	// figaro Message, though state-only tics that emit no wire
	// output have a nil RawMessage at their index.
	PerMessage []json.RawMessage

	// System is the system block array bytes — the wire form of the
	// chalkboard.system.prompt value (plus any provider-specific
	// preamble like the OAuth Claude Code identity prefix). One
	// RawMessage per system block.
	System []json.RawMessage

	// SystemFLT is the figaro logical time the System bytes are
	// tied to: the most recent figaro Message whose Patches set
	// chalkboard.system.prompt. Zero when no aria has bootstrapped
	// (ephemeral arias that synthesize a snapshot).
	SystemFLT uint64

	// Fingerprint is the provider's encoder fingerprint at the time
	// of this projection. Persisted alongside each translation entry
	// so the staleness check (D.2e) can detect encoder-config drift.
	Fingerprint string
}

// StreamEvent is a single chunk from a streaming response.
type StreamEvent struct {
	Delta       string
	ContentType message.ContentType
	BlockDone   bool
	Done        bool
	Message     *message.Message
	// Translation is set on the Done event for assistant responses:
	// it carries the per-message wire-format projection the
	// provider's NativeAccumulator produced. The agent persists it
	// to the per-aria translation log keyed by Message.LogicalTime.
	// Nil when the stream errored before message_stop.
	Translation *message.ProviderTranslation
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
	//
	// priorTranslations is the per-aria translation cache, indexed
	// in lockstep with block.Messages: priorTranslations.At(i) is
	// the cached translation for block.Messages[i]. An empty entry
	// (zero ProviderTranslation) signals a cache miss; the provider
	// renders fresh in that case. Pass an empty CausalSlice for
	// ephemeral arias.
	//
	// Returns the ProjectionSummary synchronously — the wire bytes
	// the provider just produced for this Send, ready for the agent
	// to persist into the translation log.
	Send(
		ctx context.Context,
		block *message.Block,
		snapshot chalkboard.Snapshot,
		priorTranslations causal.Slice[message.ProviderTranslation],
		tools []Tool,
		maxTokens int,
	) (<-chan StreamEvent, ProjectionSummary, error)
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
