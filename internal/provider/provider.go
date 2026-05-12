// Package provider defines the LLM provider interface.
package provider

import (
	"context"

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

// Bus is the sink for per-turn provider output.
type Bus interface {
	PushDelta(content message.Content)
	PushFigaro(msg message.Message)
	// PushToolUseStart fires when the assistant begins a tool_use block.
	PushToolUseStart(toolCallID, toolName string)
	// PushToolUseDelta carries partial input JSON. Best-effort.
	PushToolUseDelta(toolCallID, partialJSON string)
}

// SendInput is one turn's input.
type SendInput struct {
	AriaID    string
	FigLog store.Log[message.Message]
	Snapshot  chalkboard.Snapshot
	Tools     []Tool
	MaxTokens int
}

// Provider is the LLM provider interface.
type Provider interface {
	Name() string

	// Fingerprint hashes the encoder config.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	// Send drives one turn end-to-end.
	Send(ctx context.Context, in SendInput, bus Bus) error
}
