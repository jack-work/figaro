// Package provider defines the interface that LLM providers implement.
package provider

import (
	"context"
	"encoding/json"

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

// Event is what the provider pushes to the Bus during Send. Each
// event lands on the translator stream's live tail.
type Event struct {
	Payload []json.RawMessage
}

// Bus is the publish surface the provider uses while streaming.
// Implemented by the agent's Inbox.
type Bus interface {
	Push(Event)
}

// SendInput carries everything one turn needs: the per-message
// cached wire bytes (from Encode), the chalkboard snapshot the
// system prefix derives from, the tool definitions, and the token
// budget. The provider assembles the API request body internally.
type SendInput struct {
	PerMessage [][]json.RawMessage
	Snapshot   chalkboard.Snapshot
	Tools      []Tool
	MaxTokens  int
}

// Provider is the LLM provider interface. Encode projects one IR
// message into wire bytes (cached in the translator stream); Decode
// reverses (uniform over durable + live entries); Assemble folds a
// live tail into one assembled message; Send ships one turn. The
// implementation is responsible for any provider-specific request
// assembly inside Send.
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder configuration. Stored alongside
	// translator entries; mismatch invalidates them.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Encode projects one figaro IR message into native wire bytes.
	// prevSnapshot is the chalkboard state immediately before msg's
	// patches apply. Returns nil for state-only messages that emit
	// no wire output. Pure function.
	Encode(msg message.Message, prevSnapshot chalkboard.Snapshot) ([]json.RawMessage, error)

	// Decode reverses native wire bytes back into IR. Uniform over
	// durable per-message entries and live tail delta payloads —
	// the result for a delta is a partial message carrying only the
	// streamed fragment as content.
	Decode(payload []json.RawMessage) ([]message.Message, error)

	// Send ships one turn. The implementation assembles the request
	// body internally from in.PerMessage + in.Snapshot. Pushes raw
	// native delta events to the bus as they arrive; returns when
	// the stream closes. Pure transport beyond the assembly.
	Send(ctx context.Context, in SendInput, bus Bus) error

	// Assemble accumulates the live tail (sequence of raw delta
	// payloads) into the assembled assistant native message bytes.
	// Run by synchronize when the live tail is condensed.
	Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error)
}
