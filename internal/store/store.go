// Package store defines the per-aria multi-column log: canonical IR
// Stream plus per-provider translator Streams.
package store

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/message"
)

// AriaMeta is the per-aria summary at arias/{id}/meta.json.
type AriaMeta struct {
	MessageCount     int    `json:"message_count,omitempty"`
	TurnCount        int    `json:"turn_count,omitempty"` // assistant messages
	TokensIn         int    `json:"tokens_in,omitempty"`
	TokensOut        int    `json:"tokens_out,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	LastActiveMS     int64  `json:"last_active_ms,omitempty"`
	LastFigaroLT     uint64 `json:"last_figaro_lt,omitempty"`
}

// TranslationMeta is the per-provider cache summary.
type TranslationMeta struct {
	Provider     string `json:"provider"`
	EntryCount   int    `json:"entry_count,omitempty"`
	TotalBytes   int    `json:"total_bytes,omitempty"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	LastTransLT  uint64 `json:"last_trans_lt,omitempty"`
	LastUpdateMS int64  `json:"last_update_ms,omitempty"`
}

// Backend is the aria storage provider. One per angelus.
type Backend interface {
	// Open returns the figaro IR Stream for an aria.
	Open(ariaID string) (Log[message.Message], error)

	// OpenTranslation returns the per-provider translator Stream.
	OpenTranslation(ariaID, providerName string) (Log[[]json.RawMessage], error)

	// Meta returns the aria metadata, or nil if unset.
	Meta(ariaID string) (*AriaMeta, error)

	// SetMeta sets the aria metadata.
	SetMeta(ariaID string, meta *AriaMeta) error

	// TranslationMeta returns the per-provider summary.
	TranslationMeta(ariaID, providerName string) (*TranslationMeta, error)

	// SetTranslationMeta writes the per-provider translator summary.
	SetTranslationMeta(ariaID, providerName string, meta *TranslationMeta) error

	// List returns metadata for every persisted aria.
	List() ([]AriaInfo, error)

	// Remove deletes an aria. Close the agent first.
	Remove(ariaID string) error

	// Close releases backend resources.
	Close() error
}
