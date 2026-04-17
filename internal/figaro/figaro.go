// Package figaro implements the agent primitive — a long-lived AI agent
// that owns a chat context, provider, model, and prompt queue.
//
// Each figaro listens on its own unix socket and speaks JSON-RPC 2.0.
// Any client in any language can connect, send prompts, and subscribe
// to the notification stream.
//
// Currently implemented as a goroutine inside the angelus process.
// TODO: convert to child process via --figaro flag for full isolation.
package figaro

import (
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
)

// Figaro is a single agent instance.
// See package doc for lifecycle and concurrency model.
type Figaro interface {
	// ID returns the figaro's unique identifier.
	ID() string

	// SocketPath returns the path to this figaro's unix socket.
	SocketPath() string

	// Prompt enqueues a prompt. Processed FIFO by a single goroutine.
	// Returns immediately after enqueuing.
	Prompt(text string)

	// Interrupt asks the figaro to abort its current turn. Selfish
	// signal — cuts ahead of pending LLM/tool events. Idempotent and
	// safe to call when idle (no-op).
	Interrupt()

	// Context returns all messages in the chat history.
	Context() []message.Message

	// Subscribe returns a channel that receives live notifications.
	// Multiple subscribers are supported (fan-out).
	Subscribe() <-chan rpc.Notification

	// Unsubscribe removes a subscriber channel.
	Unsubscribe(ch <-chan rpc.Notification)

	// SetModel changes the model for subsequent prompts.
	SetModel(model string)

	// SetLabel sets the aria's human-readable label and persists it.
	// Empty string clears the label. Returns any persistence error.
	SetLabel(label string) error

	// Info returns current metadata.
	Info() FigaroInfo

	// Kill terminates the figaro, closes its socket, releases resources.
	Kill()
}

// FigaroInfo holds metadata about a running figaro.
type FigaroInfo struct {
	ID            string    `json:"id"`
	Label         string    `json:"label,omitempty"`
	State         string    `json:"state"` // "active", "idle"
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	MessageCount  int       `json:"message_count"`
	TokensIn      int       `json:"tokens_in"`
	TokensOut     int       `json:"tokens_out"`
	ContextTokens int       `json:"context_tokens"` // estimated next-turn input size
	ContextExact  bool      `json:"context_exact"`  // true if from Usage watermark
	CreatedAt     time.Time `json:"created_at"`
	LastActive    time.Time `json:"last_active"`
}
