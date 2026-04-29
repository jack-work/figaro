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

	// Models returns the list of models available from this provider.
	// Implementations may call the provider's API or return a static list.
	Models(ctx context.Context) ([]ModelInfo, error)

	// SetModel changes the model used for subsequent Send calls.
	SetModel(model string)

	// Send streams a conversation to the provider and returns response
	// events. Reminders is the harness's pre-rendered chalkboard
	// updates for this turn — each provider chooses how to surface them
	// per its own configuration. nil/empty = no reminders this turn.
	Send(ctx context.Context, block *message.Block, tools []Tool, reminders []chalkboard.RenderedEntry, maxTokens int) (<-chan StreamEvent, error)
}
