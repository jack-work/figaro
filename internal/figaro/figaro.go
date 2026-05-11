// Package figaro implements the agent: a long-lived process owning
// a chat context, provider, model, and chalkboard.
//
// One inbox goroutine per agent. User prompts drive synchronous
// runTurn (turn.go) which owns provider streaming and tool dispatch.
// Each figaro listens on a unix socket speaking JSON-RPC 2.0.
package figaro

import (
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// Figaro is a single agent instance.
type Figaro interface {
	ID() string
	SocketPath() string
	Prompt(text string)
	Interrupt()
	Context() []message.Message
	Info() FigaroInfo
	Kill()
}

// FigaroInfo holds metadata about a figaro.
type FigaroInfo struct {
	ID               string    `json:"id"`
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
