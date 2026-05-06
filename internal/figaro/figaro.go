// Package figaro implements the agent primitive — a long-lived AI agent
// that owns a chat context, provider, model, and a chalkboard.
//
// Concurrency: one inbox goroutine per agent. User-RPC events come in
// via the inbox; each user prompt drives a synchronous runTurn (see
// turn.go) that owns the full provider → tools → repeat-or-done
// lifecycle. Provider deltas + tool events live inside the turn,
// not on the inbox. Interrupt is a direct method that cancels the
// active turn's context.
//
// Each figaro listens on its own unix socket and speaks JSON-RPC 2.0
// — see protocol.go / server.go for the surface.
package figaro

import (
	"time"

	"github.com/jack-work/figaro/internal/message"
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

	// Info returns current metadata.
	Info() FigaroInfo

	// Kill terminates the figaro, closes its socket, releases resources.
	Kill()
}

// FigaroInfo holds metadata about a running figaro.
type FigaroInfo struct {
	ID               string    `json:"id"`
	Label            string    `json:"label,omitempty"`
	State            string    `json:"state"` // "active", "idle"
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	MessageCount     int       `json:"message_count"`
	TokensIn         int       `json:"tokens_in"`
	TokensOut        int       `json:"tokens_out"`
	CacheReadTokens  int       `json:"cache_read_tokens"`  // cumulative cache-hit tokens
	CacheWriteTokens int       `json:"cache_write_tokens"` // cumulative cache-write tokens
	ContextTokens    int       `json:"context_tokens"`     // estimated next-turn input size
	ContextExact     bool      `json:"context_exact"`      // true if from Usage watermark
	CreatedAt        time.Time `json:"created_at"`
	LastActive       time.Time `json:"last_active"`
}
