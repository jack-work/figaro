// Package store defines the per-aria multi-column log: canonical IR
// Stream plus per-provider translator Streams.
package store

import (
	"encoding/json"
	"errors"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// ErrAtStump: a Promote could not climb because the trunk is rooted directly at an outfit.
var ErrAtStump = errors.New("trunk is rooted at an outfit; cannot promote further")

var ErrNoTrunkCapability = errors.New("this figaro has no trunk capability")

// ErrWouldOrphan: the directory holds a prefix a surviving aria still reads its history through.
var ErrWouldOrphan = errors.New("delete would orphan a surviving aria")

// ErrHasBranches is checked before anything is written: the boundary repair a delete performs cannot be taken back.
var ErrHasBranches = errors.New("aria has live branches")

// VersionedPatch carries a patch's durable version: its index in the (unkeyed) form channel.
type VersionedPatch struct {
	Version uint64
	Patch   message.Patch
}

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
	// TTLMS mirrors the board's system.ttl so the sweep finds what is due without opening a node.
	TTLMS int64 `json:"ttl_ms,omitempty"`
	// MetaVersion below CurrentMetaVersion is upgraded by healIdentity on the next read.
	MetaVersion int `json:"meta_version,omitempty"`
}

// CurrentMetaVersion is the sidecar shape this build writes.
//
//	0 -> 1  form-derived identity fields (mantra, cwd, outfit).
const CurrentMetaVersion = 1

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

// OwnerInfo names the node owning a main-LT along a trunk's lineage: a parent
// trunk, an outfit (its stump name), or the genesis root.
type OwnerInfo struct {
	Trunk  string
	Outfit string
	IsRoot bool
}

// Backend owns each aria's shared log instance until Remove / Close: callers
// never close what Open returns.
type Backend interface {
	// OpenFigIR returns the canonical IR log. Every call returns the same
	// shared instance, so a live agent and a concurrent reader see the same
	// entries with lock-free reads.
	OpenFigIR(ariaID string) (Log[message.Message], error)

	// OpenTranslator returns one provider's derived translation of the IR;
	// each entry names the IR entry it translates.
	OpenTranslator(ariaID, providerName string) (Log[[]json.RawMessage], error)

	// CloseOpenToolCalls closes every outstanding tool invoke with an error
	// result and reports how many. The IR write path does this on its next
	// append anyway; the interrupt path calls it so history is well-formed the
	// moment the turn ends.
	CloseOpenToolCalls(ariaID string) (int, error)

	// FormState folds the reducible form channel; the channel is the durable
	// truth, there is no separate form file.
	FormState(ariaID string) (form.Snapshot, error)

	// FormVersion is the durable index of the last patch appended: the version a conditional Set quotes.
	FormVersion(ariaID string) (uint64, error)

	// LastTS is the newest record timestamp in the node (unix millis), from
	// figwal's lock-free counter; it never wakes an agent. Zero for
	// pre-timestamp history.
	LastTS(id string) int64

	// LoadDefaultForm returns (nil, nil) before first mint.
	LoadDefaultForm() (*DefaultFormRecord, error)
	SaveDefaultForm(rec *DefaultFormRecord) error

	// WatchForm's sink is called on the form's writer, so it must hand off and
	// return. It is registered on one Form instance and dies with it on
	// eviction; a long-lived reader of another aria's form wants
	// Form.SubscribeFrom, whose subscription is a lease the sweep honours.
	WatchForm(ariaID string, fn func(version uint64, patch message.Patch)) error

	// SetObservedForms declares the forms whose positions every IR append
	// stamps. The board's system.studies is the durable truth; this is its
	// in-memory mirror, re-declared on boot and on study/drop.
	SetObservedForms(ariaID string, formIDs []string)

	// ForkWith forks a node and lands a patch on the child in one critical
	// section. parent == "" forks the null root.
	ForkWith(parent string, atMainLT uint64, patch message.Patch) (child string, version uint64, err error)

	// CreateForm mints an unbound form from the null root ("") or another
	// form; a conversation parent is refused.
	CreateForm(parent string, patch message.Patch) (id string, version uint64, err error)

	// ApplyFormEffectIntent appends unless the form has moved off ifVersion
	// (zero is unconditional; compared in the writer, atomically with the
	// append) and returns the reduced patch that actually landed, which is
	// what any fan-out must report. Under Assert a removal of an absent key is
	// refused rather than reduced away.
	ApplyFormEffectIntent(ariaID string, patch message.Patch, ifVersion uint64, intent Intent) (uint64, message.Patch, error)

	// ApplyFormPrivileged may touch system-managed keys. There is no wire
	// field for this and must never be one: privilege is a property of the
	// call site.
	ApplyFormPrivileged(ariaID string, patch message.Patch) (uint64, error)

	// ApplyForm appends a patch keyed to the next IR LT.
	ApplyForm(ariaID string, patch message.Patch) (version uint64, err error)

	// FormPatchesBetween returns the patches in (after, upTo] by durable version.
	FormPatchesBetween(ariaID string, after, upTo uint64) ([]VersionedPatch, error)

	// CreateOutfit materializes or reuses the node for (name, content version of patch).
	CreateOutfit(name string, patch message.Patch) (string, error)

	CreateConversation(outfitID string) (string, error)

	// Fork branches at the head: the node freezes and keeps its id as an
	// index node; both children get fresh ids.
	Fork(ariaID string) (cont, alt string, err error)

	// ForkAt freezes the prefix below atMainLT; the original suffix becomes
	// the continuation and a fresh alternative starts empty. Both children
	// get fresh ids.
	ForkAt(ariaID string, atMainLT uint64) (cont, alt string, err error)

	// Promote climbs up to `levels` stump-bounded levels and returns how many
	// it climbed; ErrAtStump when rooted at an outfit.
	Promote(ariaID string, levels int) (climbed int, err error)

	// Normalize runs deferred topology work now: blocking, O(absorbed bytes).
	Normalize() (detached int, err error)

	OwnerResolution(ariaID string, atMainLT uint64) (OwnerInfo, error)

	Node(id string) (NodeView, bool)
	Nodes() []NodeView

	// Forms returns every unbound form trunk.
	Forms() []NodeView

	// CollectStump removes a childless outfit stump; stumps are content
	// addressed, so the next aria wanting that outfit re-mints it identically.
	CollectStump(stumpID string) error
	Conversations() []NodeView
	ConversationIDs() []string

	// Meta returns nil if unset.
	Meta(ariaID string) (*AriaMeta, error)

	SetMeta(ariaID string, meta *AriaMeta) error

	// UpdateMeta mutates under the sidecar's own lock: the record has more
	// than one writer, and a whole-record write from either drops the other's.
	UpdateMeta(ariaID string, mutate func(*AriaMeta)) error

	// Remove deletes a trunk's subtree; close the agent first. Without
	// recursive, a trunk with live branches is refused.
	Remove(ariaID string, recursive bool) error

	Close() error
}
