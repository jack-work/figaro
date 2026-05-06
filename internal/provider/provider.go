// Package provider defines the interface that LLM providers implement.
//
// Stage 1 of the actor refactor: the provider owns synchronize. It
// receives the figStream + translator stream directly, catches up the
// translator before sending, streams deltas through the bus while the
// HTTP response arrives, and on EOF condenses the live tail into a
// durable assistant message — all without the agent reaching across
// the abstraction.
package provider

import (
	"context"
	"encoding/json"

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
// Inbox implements it: PushDelta drives UX streaming; PushFigaro
// signals that an assembled assistant message has landed in figStream
// (the provider has already written it).
type Bus interface {
	PushDelta(content message.Content)
	PushFigaro(msg message.Message)
}

// SendInput is one turn's input. The provider catches up the
// translator from FigStream, builds the request body from
// Translator.Durable + Snapshot, POSTs, streams deltas through bus,
// and on EOF appends the assembled assistant message to FigStream
// and the input-ready bytes to Translator.
type SendInput struct {
	FigStream  store.Stream[message.Message]
	Translator store.Stream[[]json.RawMessage]
	Snapshot   chalkboard.Snapshot
	Tools      []Tool
	MaxTokens  int
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
