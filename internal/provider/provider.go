// Package provider defines the interface that model providers must implement.
//
// A Provider is an isomorphism between figaro's canonical message types
// and a provider's native wire format. It can project canonical messages
// onto its native format (for sending to the API) and lift native
// responses back into canonical form (for storage and display).
//
// Providers may use the Baggage map on messages to cache their native
// representations, avoiding redundant conversion on subsequent turns.
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
	Delta       string
	ContentType message.ContentType
	BlockDone   bool
	Done        bool
	Message     *message.Message
	Err         error
}

// Request captures everything needed for a single LLM call.
type Request struct {
	SystemPrompt string
	Messages     []message.Message
	Tools        []Tool
	MaxTokens    int
}

// Provider is the interface each model backend must implement.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
