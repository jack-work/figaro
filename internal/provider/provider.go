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
// Encoding is split into per-message projection (EncodeMessage,
// cached in the translation stream) and request assembly
// (AssembleRequest, builds the API body from cached entries plus the
// current snapshot). Send is pure transport. Live deltas are
// interpreted by DecodeDelta (UI surface) and Assemble (end-of-turn
// condense from the live tail).
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder configuration. Stored alongside
	// translation entries; mismatch invalidates them.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)

	// agent: provider shouldn't really be stateful.  We should respect the model that is seen on the input whenever sending
	// messages.  The input to Send should be in the format necessary to build the API request.  Ideally raw byte marshalling,
	// or a body + headers.  All that should be included in the translator stream (which by the way you should rename "TranslatorStream")
	// Trans log is not a good name.  If you read this, eradicate it.
	SetModel(model string)

	// agent: Just call it encode.
	// EncodeMessage projects one figaro IR message into native wire
	// bytes. prevSnapshot is the chalkboard state immediately before
	// msg's patches apply. Returns nil for state-only messages that
	// emit no wire output. Pure function.
	EncodeMessage(msg message.Message, prevSnapshot chalkboard.Snapshot) ([]json.RawMessage, error)

	// agent: just make Send accept json.RawMessage and implement this in the implementing type.
	AssembleRequest(perMessage [][]json.RawMessage, snapshot chalkboard.Snapshot, tools []Tool, maxTokens int) ([]byte, error)

	// Decode reverses native wire bytes back into IR.
	Decode(raw []json.RawMessage) ([]message.Message, error)

	// Send POSTs the pre-encoded body, pushes raw native delta events
	// to the bus as they arrive, and returns when the stream closes.
	// Pure transport — no encoding, no accumulation.
	Send(ctx context.Context, body []byte, bus Bus) error

	// agent:  just make one decode that returns optional text.
	//         the decoding operations and the encoding should not be aware whether they
	//         are viewing the live tail or the durable entries.  They should just see a flat
	//         stream.  there should be less special casing in the stream
	DecodeDelta(payload []json.RawMessage) (text string, ct message.ContentType, ok bool)

	// Assemble accumulates the live tail (sequence of raw delta
	// payloads) into the assembled assistant native message bytes.
	// Run by synchronize when the live tail is condensed.
	Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error)
}
