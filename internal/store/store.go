// Package store defines the per-aria multi-column log: canonical IR
// Stream plus per-provider translator Streams.
package store

import (
	"encoding/json"
	"errors"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// ErrAtStump means a Promote could not climb at all: the trunk is rooted
// directly at an outfit (the cauterization boundary). Callers map it to a
// domain message ("cannot promote into an outfit; make/edit an outfit").
var ErrAtStump = errors.New("trunk is rooted at an outfit; cannot promote further")

// ErrNoTrunkCapability reports a figaro built without the presentation
// hierarchy: promotion is meaningless, not merely refused.
var ErrNoTrunkCapability = errors.New("this figaro has no trunk capability")

// ErrWouldOrphan reports a delete that would strand a surviving aria: the
// directory holds a prefix that aria still reads its history through.
// Promotion is what lets the two hierarchies diverge far enough for this.
var ErrWouldOrphan = errors.New("delete would orphan a surviving aria")

// VersionedPatch is a form patch with its durable version -- its own
// index in the form channel. The board is unkeyed, so this is the
// only coordinate a patch carries.
type VersionedPatch struct {
	Version uint64
	Patch   message.Patch
}

// AriaMeta is the per-aria summary stored by the backend.
type AriaMeta struct {
	MessageCount     int    `json:"message_count,omitempty"`
	TurnCount        int    `json:"turn_count,omitempty"` // assistant messages
	TokensIn         int    `json:"tokens_in,omitempty"`
	TokensOut        int    `json:"tokens_out,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	LastFigaroLT     uint64 `json:"last_figaro_lt,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Mantra           string `json:"mantra,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	OutfitName       string `json:"outfit_name,omitempty"`
	OutfitVersion    string `json:"outfit_version,omitempty"`
	ContextTokens    int    `json:"context_tokens,omitempty"`
	ContextLimit     int    `json:"context_limit,omitempty"`
	ContextExact     bool   `json:"context_exact,omitempty"`
	CreatedAtMS      int64  `json:"created_at_ms,omitempty"`
}

// UnmarshalJSON accepts the pre-rename loadout_* keys existing sidecars carry;
// only outfit_* is written back.
func (m *AriaMeta) UnmarshalJSON(b []byte) error {
	type alias AriaMeta
	var wire struct {
		alias
		LegacyOutfitName    string `json:"loadout_name,omitempty"`
		LegacyOutfitVersion string `json:"loadout_version,omitempty"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*m = AriaMeta(wire.alias)
	if m.OutfitName == "" {
		m.OutfitName = wire.LegacyOutfitName
	}
	if m.OutfitVersion == "" {
		m.OutfitVersion = wire.LegacyOutfitVersion
	}
	return nil
}

// OwnerInfo describes which node owns a main-LT along a trunk's lineage:
// a parent trunk (Trunk set), an outfit (Outfit set, its stump name), or
// the genesis root (IsRoot). Used for the <id>:<LT> addressing announcement.
type OwnerInfo struct {
	Trunk  string
	Outfit string
	IsRoot bool
}

// Backend is the aria storage provider. One per angelus. The only
// implementation is *XwalBackend (the fork-tree on figwal/xwal); it
// owns each aria's shared log instance until Remove / Close: callers
// never close what Open returns.
type Backend interface {
	// Open returns the figaro IR Stream for an aria. The same shared,
	// memoized instance is returned for every call (so a live agent
	// and a concurrent aria.read see the same rows, lock-free reads).
	Open(ariaID string) (Log[message.Message], error)

	// OpenTranslation returns the per-provider translator Stream.
	OpenTranslation(ariaID, providerName string) (Log[[]json.RawMessage], error)

	// Kick expedites the store's background flush: called after appends
	// worth making durable sooner than the flush interval (user tics).
	Kick()

	// FormState folds the aria's reducible form channel to
	// its current snapshot. The channel is the durable truth; there is
	// no separate form file.
	FormState(ariaID string) (form.Snapshot, error)

	// FormVersion is the durable index of the last patch appended to
	// the aria's form channel: the version a conditional Set quotes.
	FormVersion(ariaID string) (uint64, error)

	// LastTS is the newest figwal record timestamp anywhere in the node,
	// unix millis: the recency a listing sorts by. Served from figwal's
	// lock-free per-node counter; NEVER wakes an agent (it opens a store
	// handle, not a figaro). Zero for pre-timestamp history.
	LastTS(id string) int64

	// KeepStump names the stump collection must spare: the live default, whose
	// re-minting would rewrite a whole outfit to save one directory.
	// Legacy: new arias are born of the DEFAULT FORM, not a stump.
	KeepStump(id string)

	// LoadDefaultForm / SaveDefaultForm read and write the daemon's
	// pointer to the current default form: `fig new`'s forking point and
	// KeepStump's successor. Load returns (nil, nil) before first mint.
	LoadDefaultForm() (*DefaultFormRecord, error)
	SaveDefaultForm(rec *DefaultFormRecord) error

	// WatchForm registers a sink for every patch committed to an aria's form.
	// Called on the form's writer, so a sink must hand off and return.
	WatchForm(ariaID string, fn func(version uint64, patch message.Patch)) error

	// WatchFormDurable is WatchForm surviving eviction: re-armed whenever
	// the node's Form reopens. The cancel removes the registration; the
	// DELIVERY gate remains the caller's (a cancelled sink may fire until
	// the live Form closes).
	// SetObservedForms declares the forms whose positions every IR append
	// of this aria stamps (the observed set: study subscriptions). The
	// board's system.studies is the durable truth; this is its in-memory
	// mirror, re-declared by the agent on boot and on study/drop.
	SetObservedForms(ariaID string, formIDs []string)

	// ForkWith forks a node and lands a patch on the child in one critical
	// section. parent == "" forks the null root, which is what birth is.
	ForkWith(parent string, atMainLT uint64, patch message.Patch) (child string, version uint64, err error)

	// CreateForm mints an UNBOUND FORM: fork the null root (parent "") or
	// another form, with a birth patch, kind "form", @-sigiled id. Only
	// forms fork independently; a conversation parent is refused.
	CreateForm(parent string, patch message.Patch) (id string, version uint64, err error)

	// ApplyFormIf appends a patch unless the form has moved off ifVersion
	// (zero applies unconditionally). The comparison happens in the form's
	// writer, atomically with the append.
	ApplyFormIf(ariaID string, patch message.Patch, ifVersion uint64) (uint64, error)

	// ApplyFormEffect is ApplyFormIf plus what actually landed: the writer
	// reduces a patch against the board (a key already holding the value is
	// not an event), and a caller that reports or fans out the change must
	// speak about the reduced patch, not the requested one.
	ApplyFormEffect(ariaID string, patch message.Patch, ifVersion uint64) (uint64, message.Patch, error)

	// ApplyForm appends a state patch to the form channel,
	// keyed to the next IR LT (the transition the next message carries).
	ApplyForm(ariaID string, patch message.Patch) (version uint64, err error)

	// FormPatches returns every form patch grouped by the IR
	// logical time it is keyed to (the transitions to render per message).
	// Empty patches (genesis/seed no-ops) are omitted.
	FormPatches(ariaID string) ([]VersionedPatch, error)

	// CreateOutfit materializes (or reuses) the outfit node for
	// (name, content-version-of-patch) and returns its id.
	CreateOutfit(name string, patch message.Patch) (string, error)

	// CreateConversation forks an outfit node into a fresh conversation.
	CreateConversation(outfitID string) (string, error)

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
	// actually climbed; xwal.ErrAtStump means it is rooted at an outfit.
	Promote(ariaID string, levels int) (climbed int, err error)

	// Normalize runs deferred topology work now: every aria presented away
	// from where its history lives absorbs that history. Blocking, and
	// O(absorbed bytes) -- the only operation here that is not instant.
	Normalize() (detached int, err error)

	// OwnerResolution reports which node owns atMainLT along a trunk's
	// lineage (a parent trunk, an outfit, or the genesis root).
	OwnerResolution(ariaID string, atMainLT uint64) (OwnerInfo, error)

	// Node / Nodes expose the tree for lineage + listing.
	Node(id string) (NodeView, bool)
	Nodes() []NodeView

	// Forms returns every unbound form trunk (kind "form").
	Forms() []NodeView

	// CollectStump removes a childless outfit stump. Stumps are content
	// addressed, so a collected one is re-minted identically by the next aria
	// that wants that outfit; this reclaims the versions nothing uses.
	CollectStump(stumpID string) error
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
