// Package provider defines the interface that LLM providers implement.
//
// A provider is the reverse of a store: where the store receives
// messages and returns context, the provider receives context and
// returns messages.
//
// The provider handles conversion between figaro's IR (message.Block)
// and its native wire format internally. When a message in the block
// has baggage for this provider, the provider uses that directly
// instead of re-converting from the IR.
//
// The provider wraps each response chunk in the IR, including the
// unaltered original representation in the baggage field. The caller
// never touches native provider types.
package provider

import (
	"context"

	"github.com/jack-work/figaro/internal/message"
)

// Tool describes a tool the model can call.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"` // JSON Schema
}

// StreamEvent is a single chunk from a streaming response.
type StreamEvent struct {
	// Delta text for incremental display.
	Delta string

	// ContentType of the block being streamed.
	ContentType message.ContentType

	// BlockDone is true when a content block is complete.
	BlockDone bool

	// Done is true when the entire response is finished.
	// Message is the final accumulated IR message with baggage populated.
	Done bool

	// Message is the accumulated IR message (partial during stream, final when Done).
	// Baggage is populated with the provider's native representation on Done.
	Message *message.Message

	// Err is non-nil only when Done && the response ended in error.
	Err error
}

// Provider is the LLM provider interface.
//
// It receives a conversation block (IR), converts it to its native
// format (using baggage when available), calls the API, and streams
// back IR messages with baggage populated.
type Provider interface {
	// Name returns the provider identifier (e.g. "anthropic").
	// Used as the key in Message.Baggage.
	Name() string

	// Send provides the conversation context and available tools
	// to the provider. The provider converts the block to its
	// native format internally, using baggage where available.
	//
	// Returns a channel that streams response events.
	// The channel is closed after the final event (Done == true).
	//
	// Each StreamEvent.Message has Baggage[Name()] populated with
	// the unaltered native response, so re-sending this message
	// back to the same provider is a cache hit.
	Send(ctx context.Context, block *message.Block, tools []Tool, maxTokens int) (<-chan StreamEvent, error)
}
