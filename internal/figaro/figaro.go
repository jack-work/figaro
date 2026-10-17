// Package figaro implements the agent: a long-lived actor owning a chat context, provider, model, and form.
package figaro

import (
	"time"
)

type Figaro interface {
	ID() string
	Interrupt()
	Info() FigaroInfo
	Kill()
}

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
	ContextLimit     int       `json:"context_limit"`      // effective prompt cap when known
	ContextExact     bool      `json:"context_exact"`      // true if from Usage watermark
	CreatedAt        time.Time `json:"created_at"`
	LastActive       time.Time `json:"last_active"`
	// AgentSince is this agent's construction (birth or wake); LastActive is the
	// last turn. Reclamation reads the later of the two.
	AgentSince time.Time `json:"agent_since"`
	// WokeBy names the RPC method that restored a dormant aria, so a poller that
	// keeps waking arias is named in the log.
	WokeBy        string `json:"woke_by,omitempty"`
	Mantra        string `json:"mantra"`
	Cwd           string `json:"cwd"`
	OutfitName    string `json:"outfit_name"`
	OutfitVersion string `json:"outfit_version"`
	LastFigaroLT  uint64 `json:"last_figaro_lt"`
}
