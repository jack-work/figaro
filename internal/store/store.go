// Package store defines the per-aria multi-column log: canonical IR
// Stream plus per-provider translator Streams.
package store

import (
	"encoding/json"
	"errors"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// ErrAtStump means a Promote could not climb at all: the trunk is rooted
// directly at a loadout (the cauterization boundary). Callers map it to a
// domain message ("cannot promote into a loadout; make/edit a loadout").
var ErrAtStump = errors.New("trunk is rooted at a loadout; cannot promote further")

// AriaMeta is the per-aria summary stored by the backend.
type AriaMeta struct {
	MessageCount     int    `json:"message_count,omitempty"`
	TurnCount        int    `json:"turn_count,omitempty"` // assistant messages
	TokensIn         int    `json:"tokens_in,omitempty"`
	TokensOut        int    `json:"tokens_out,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	LastActiveMS     int64  `json:"last_active_ms,omitempty"`
	LastFigaroLT     uint64 `json:"last_figaro_lt,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Mantra           string `json:"mantra,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	LoadoutName      string `json:"loadout_name,omitempty"`
	LoadoutVersion   string `json:"loadout_version,omitempty"`
	ContextTokens    int    `json:"context_tokens,omitempty"`
	ContextLimit     int    `json:"context_limit,omitempty"`
	ContextExact     bool   `json:"context_exact,omitempty"`
	CreatedAtMS      int64  `json:"created_at_ms,omitempty"`
}

// OwnerInfo describes which node owns a main-LT along a trunk's lineage:
// a parent trunk (Trunk set), a loadout (Loadout set, its stump name), or
// the genesis root (IsRoot). Used for the <id>:<LT> addressing announcement.
type OwnerInfo struct {
	Trunk   string
	Loadout string
	IsRoot  bool
}

// Backend is the aria storage provider. One per angelus. The only
// implementation is *XwalBackend (the fork-tree on figwal/xwal); it
// owns each aria's shared log instance until Remove / Close — callers
// never close what Open returns.
type Backend interface {
	// Open returns the figaro IR Stream for an aria. The same shared,
	// memoized instance is returned for every call (so a live agent
	// and a concurrent aria.read see the same rows, lock-free reads).
	Open(ariaID string) (Log[message.Message], error)

	// OpenTranslation returns the per-provider translator Stream.
	OpenTranslation(ariaID, providerName string) (Log[[]json.RawMessage], error)

	// Kick expedites the store's background flush — called after appends
	// worth making durable sooner than the flush interval (user tics).
	Kick()

	// ChalkboardState folds the aria's reducible chalkboard channel to
	// its current snapshot. The channel is the durable truth; there is
	// no separate chalkboard file.
	ChalkboardState(ariaID string) (chalkboard.Snapshot, error)

	// ApplyChalkboard appends a state patch to the chalkboard channel,
	// keyed to the next IR LT (the transition the next message carries).
	ApplyChalkboard(ariaID string, patch message.Patch) error

	// ChalkboardPatches returns every chalkboard patch grouped by the IR
	// logical time it is keyed to (the transitions to render per message).
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

	// ForkAt branches a conversation at main-LT atMainLT (an interior
	// fork): the shared prefix below atMainLT freezes, the original
	// suffix becomes the continuation, and a fresh alternative starts
	// empty from atMainLT. Both children get fresh ids.
	ForkAt(ariaID string, atMainLT uint64) (cont, alt string, err error)

	// Promote climbs a conversation trunk up `levels` stump-bounded levels
	// (it absorbs its parent trunk's run). Returns the number of levels
	// actually climbed; xwal.ErrAtStump means it is rooted at a loadout.
	Promote(ariaID string, levels int) (climbed int, err error)

	// OwnerResolution reports which node owns atMainLT along a trunk's
	// lineage (a parent trunk, a loadout, or the genesis root).
	OwnerResolution(ariaID string, atMainLT uint64) (OwnerInfo, error)

	// Node / Nodes expose the tree for lineage + listing.
	Node(id string) (NodeView, bool)
	Nodes() []NodeView
	Conversations() []NodeView
	ConversationIDs() []string

	// Meta returns the aria metadata, or nil if unset.
	Meta(ariaID string) (*AriaMeta, error)

	// SetMeta sets the aria metadata.
	SetMeta(ariaID string, meta *AriaMeta) error

	// Remove deletes a trunk (its subtree). Close the agent first. recursive
	// also removes any live branches; without it, a trunk with branches is
	// refused.
	Remove(ariaID string, recursive bool) error

	// Close releases backend resources.
	Close() error
}
