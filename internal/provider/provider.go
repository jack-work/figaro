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

// Event is one Push to the Bus during Send. Lands on the translator
// stream's live tail.
type Event struct {
	Payload []json.RawMessage
}

// Bus is the publish surface for streaming providers. Implemented
// by the agent's Inbox.
type Bus interface {
	Push(Event)
}

// SendInput is one turn's input: cached per-message bytes (from
// Encode), the chalkboard snapshot the system prefix derives from,
// tool defs, and the token budget. The provider assembles the
// request body internally.
type SendInput struct {
	PerMessage [][]json.RawMessage
	Snapshot   chalkboard.Snapshot
	Tools      []Tool
	MaxTokens  int
}

// Provider is the LLM provider interface. Encode + Decode + Assemble
// + Send. The implementation owns request assembly.
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder config. Mismatch invalidates
	// translator entries.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Encode projects one IR message into native wire bytes.
	// prevSnapshot is the chalkboard state before msg's patches.
	// Returns nil for state-only messages.
	Encode(msg message.Message, prevSnapshot chalkboard.Snapshot) ([]json.RawMessage, error)

	// Decode reverses native bytes back to IR. Uniform over durable
	// per-message entries and live tail delta payloads — the result
	// for a delta is a partial Message with only the streamed
	// fragment as content.
	Decode(payload []json.RawMessage) ([]message.Message, error)

	// Send ships one turn: assembles the request body from
	// in.PerMessage + in.Snapshot, POSTs, pushes raw native deltas
	// to the bus, returns when the stream closes.
	Send(ctx context.Context, in SendInput, bus Bus) error

	// Assemble folds the live tail into the assembled assistant
	// nativeMessage bytes. Run by synchronize at condense time.
	Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error)
}
