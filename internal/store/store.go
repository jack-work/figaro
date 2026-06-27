// Package store defines the per-aria multi-column log: canonical IR
// Stream plus per-provider translator Streams.
package store

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/chalkboard"
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

// Backend is the aria storage provider. One per angelus. The only
// implementation is *XwalBackend (the fork-tree on figwal/xwal); it
// owns each aria's shared log instance and closes it on Fork / Remove
// / Close — callers never close what Open returns.
type Backend interface {
	// Open returns the figaro IR Stream for an aria. The same shared,
	// memoized instance is returned for every call (so a live agent
	// and a concurrent aria.read see the same rows, lock-free reads).
	Open(ariaID string) (Log[message.Message], error)

	// OpenTranslation returns the per-provider translator Stream.
	OpenTranslation(ariaID, providerName string) (Log[[]json.RawMessage], error)

	// ChalkboardState folds the aria's reducible chalkboard channel to
	// its current snapshot. The channel is the durable truth; there is
	// no separate chalkboard file.
	ChalkboardState(ariaID string) (chalkboard.Snapshot, error)

	// ApplyChalkboard appends a state patch to the chalkboard channel,
	// keyed to the next IR LT (the transition the next tic carries).
	ApplyChalkboard(ariaID string, patch message.Patch) error

	// ChalkboardPatches returns every chalkboard patch grouped by the IR
	// logical time it is keyed to (the transitions to render per tic).
	// Empty patches (genesis/seed no-ops) are omitted.
	ChalkboardPatches(ariaID string) (map[uint64][]message.Patch, error)

	// CreateLoadout materializes (or reuses) the loadout node for
	// (name, content-version-of-patch) and returns its id.
	CreateLoadout(name string, patch message.Patch) (string, error)

	// CreateConversation forks a loadout node into a fresh conversation.
	CreateConversation(loadoutID string) (string, error)

	// Fork branches a conversation at its head: the node freezes and
	// keeps its id as an index node; both children get fresh ids.
	Fork(ariaID string) (cont, alt string, err error)

	// Node / Nodes expose the tree for lineage + listing.
	Node(id string) (NodeView, bool)
	Nodes() []NodeView

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
