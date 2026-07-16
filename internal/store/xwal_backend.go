package store

// XwalBackend implements store.Backend over the XwalStore aria tree.
// It memoizes one cachedLog per (aria, channel) so an agent's read grip
// is cheap and stable. It does NOT cache *xwal.XWAL handles: every read
// and write opens a fresh one via s.trunks.Head / .Append / .AppendChannel,
// which serialize against Fork/Promote inside figwal. No eviction dance
// is needed — a Fork on aria X does not invalidate the row cache (the
// bytes on disk are stable append-only truth), and a Promote is purely
// cosmetic. The old evictAll on Promote was over-scoped and could strand
// a live agent mid-turn ("file already closed"); it's gone.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figwal/xwal"
)

var _ Backend = (*XwalBackend)(nil)

type XwalBackend struct {
	root  string
	store *XwalStore
	mu    sync.Mutex
	open  map[string]*ariaHandle
}

type ariaHandle struct {
	id    string
	ir    *cachedLog[message.Message]
	trans map[string]*cachedLog[[]json.RawMessage]
}

func NewXwalBackend(root string) (*XwalBackend, error) {
	st, err := OpenXwalStore(root)
	if err != nil {
		return nil, err
	}
	return &XwalBackend{root: root, store: st, open: map[string]*ariaHandle{}}, nil
}

// Store exposes the underlying tree (create/fork/list) to the daemon.
func (b *XwalBackend) Store() *XwalStore { return b.store }

// handleLocked returns the shared handle for an aria, opening it once.
// Caller holds b.mu. The handle carries the row caches for the aria's
// channels; nothing else. Fresh *xwal.XWAL instances are opened on
// demand by the xwalLog inside each cachedLog.
func (b *XwalBackend) handleLocked(id string) (*ariaHandle, error) {
	if h := b.open[id]; h != nil {
		return h, nil
	}
	// Sanity-open once at handle creation to fail fast if the aria is
	// unknown; the underlying xwal is closed immediately after.
	xw, err := b.store.OpenNode(id)
	if err != nil {
		return nil, err
	}
	_ = xw.Close()
	h := &ariaHandle{
		id:    id,
		ir:    newCachedLog[message.Message](newXwalLog[message.Message](b.store, id, chanIR, true)),
		trans: map[string]*cachedLog[[]json.RawMessage]{},
	}
	b.open[id] = h
	return h, nil
}

func (b *XwalBackend) Open(ariaID string) (Log[message.Message], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, err := b.handleLocked(ariaID)
	if err != nil {
		return nil, err
	}
	return h.ir, nil
}

func transChannel(provider string) string { return "translations/" + provider }

func (b *XwalBackend) OpenTranslation(ariaID, providerName string) (Log[[]json.RawMessage], error) {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if c := h.trans[providerName]; c != nil {
		b.mu.Unlock()
		return c, nil
	}
	b.mu.Unlock()
	ch := transChannel(providerName)
	// Materialize the channel on disk if it doesn't exist yet. Open a
	// fresh xwal, add, close.
	if err := b.ensureChannel(ariaID, ch); err != nil {
		return nil, err
	}
	c := newCachedLog[[]json.RawMessage](newXwalLog[[]json.RawMessage](b.store, ariaID, ch, false))
	b.mu.Lock()
	if existing := h.trans[providerName]; existing != nil {
		b.mu.Unlock()
		return existing, nil
	}
	h.trans[providerName] = c
	b.mu.Unlock()
	return c, nil
}

func (b *XwalBackend) ensureChannel(ariaID, ch string) error {
	xw, err := b.store.OpenNode(ariaID)
	if err != nil {
		return err
	}
	defer xw.Close()
	if channelExists(xw, ch) {
		return nil
	}
	return xw.AddChannel(xwal.ChannelSpec{Name: ch, Kind: xwal.ChannelLog})
}

func channelExists(x *xwal.XWAL, name string) bool {
	for _, c := range x.Channels() {
		if c.Name == name {
			return true
		}
	}
	return false
}

// ---- chalkboard (re-derived via StateAt; mutation appends a patch) ----

// ChalkboardState folds the aria's chalkboard channel to current state.
// No memoization here (previously cached on the handle against the
// channel tail; the win is small compared to a fresh Open-fold-Close,
// and correctness under Fork/Promote is easier without it).
func (b *XwalBackend) ChalkboardState(ariaID string) (chalkboard.Snapshot, error) {
	xw, err := b.store.OpenNode(ariaID)
	if err != nil {
		return nil, err
	}
	defer xw.Close()
	last := channelLast(xw, chanChalkboard)
	if last == 0 {
		return chalkboard.Snapshot{}, nil
	}
	st, err := xw.StateAt(chanChalkboard, last)
	if err != nil {
		return nil, err
	}
	snap := chalkboard.Snapshot{}
	if err := json.Unmarshal(st, &snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// ChalkboardPatches reads the whole chalkboard channel once and groups
// the (non-empty) patches by the IR LT they are keyed to.
func (b *XwalBackend) ChalkboardPatches(ariaID string) (map[uint64][]message.Patch, error) {
	t0 := time.Now()
	xw, err := b.store.OpenNode(ariaID)
	if err != nil {
		return nil, err
	}
	defer xw.Close()
	var first, last uint64
	for _, c := range xw.Channels() {
		if c.Name == chanChalkboard {
			first, last = c.First, c.Last
		}
	}
	out := map[uint64][]message.Patch{}
	for lt := first; lt >= 1 && lt <= last; lt++ {
		rec, err := xw.ReadAt(chanChalkboard, lt)
		if err != nil {
			return nil, err
		}
		var p message.Patch
		if err := json.Unmarshal(rec.Payload, &p); err != nil {
			return nil, err
		}
		if p.IsEmpty() {
			continue
		}
		out[rec.MainLT] = append(out[rec.MainLT], p)
	}
	if elapsed := time.Since(t0); elapsed > 100*time.Millisecond {
		slog.Warn("ChalkboardPatches slow", "aria", ariaID, "entries", last-first+1, "elapsed", elapsed)
	}
	return out, nil
}

// ApplyChalkboard appends a patch to the chalkboard channel, keyed to the
// next IR LT (a set records state for the turn about to happen). Routes
// through Trunks.AppendChannel so it serializes with Fork/Promote.
func (b *XwalBackend) ApplyChalkboard(ariaID string, patch message.Patch) error {
	pb, _ := json.Marshal(patch)
	// Pass mainLT=0 to let Trunks compute (mainTail+1) internally; the
	// chalkboard channel is reducible/keyed-forward and the default
	// "one ahead" semantics match the previous channelLast+1 behavior.
	_, err := b.store.trunks.AppendChannel(ariaID, chanChalkboard, 0, pb, nil)
	return err
}

func channelLast(x *xwal.XWAL, name string) uint64 {
	for _, c := range x.Channels() {
		if c.Name == name {
			return c.Last
		}
	}
	return 0
}

// ---- tree operations (delegated) ----

func (b *XwalBackend) CreateLoadout(name string, patch message.Patch) (string, error) {
	return b.store.CreateLoadout(name, patch)
}
func (b *XwalBackend) CreateConversation(loadoutID string) (string, error) {
	return b.store.CreateConversation(loadoutID)
}
func (b *XwalBackend) Fork(ariaID string) (cont, alt string, err error) {
	return b.store.Fork(ariaID)
}
func (b *XwalBackend) ForkAt(ariaID string, atMainLT uint64) (cont, alt string, err error) {
	return b.store.ForkAt(ariaID, atMainLT)
}

// Promote climbs a conversation trunk. Cosmetic: relabels ancestor
// .trunk markers, no content moves, cached rows stay valid.
func (b *XwalBackend) Promote(ariaID string, levels int) (int, error) {
	return b.store.Promote(ariaID, levels)
}

func (b *XwalBackend) OwnerResolution(ariaID string, atMainLT uint64) (OwnerInfo, error) {
	o, err := b.store.OwnerOf(ariaID, atMainLT)
	if err != nil {
		return OwnerInfo{}, err
	}
	return OwnerInfo{Trunk: o.Trunk, Loadout: o.Stump, IsRoot: o.IsRoot}, nil
}

func (b *XwalBackend) Node(id string) (NodeView, bool) { return b.store.Node(id) }
func (b *XwalBackend) Nodes() []NodeView               { return b.store.Nodes() }

// CanonicalCount recomputes the conversational message count from the aria's
// live head IR (message.CountMessages — the shared derivation) and self-heals
// a stale _meta sidecar that disagrees. The head is a single deterministic
// leaf (figwal multi-head fix + heal), so this is order-independent.
func (b *XwalBackend) CanonicalCount(id string) (int, bool) {
	c, ok := b.canonicalCount(id)
	if !ok {
		return 0, false
	}
	if m, _ := b.Meta(id); m != nil && m.MessageCount != c {
		mm := *m
		mm.MessageCount = c
		_ = b.SetMeta(id, &mm)
	}
	return c, true
}

// dropHandle removes the aria's handle shell from the open map. Used by
// Remove after the trunk is gone. No xwal to close — handles don't own
// any.
func (b *XwalBackend) dropHandle(id string) {
	b.mu.Lock()
	delete(b.open, id)
	b.mu.Unlock()
}

// ---- metadata (sidecar JSON at root/_meta) ----

func (b *XwalBackend) metaPath(id string) string {
	return filepath.Join(b.root, "_meta", id+".json")
}
func (b *XwalBackend) tmetaPath(id, provider string) string {
	return filepath.Join(b.root, "_meta", id+"."+provider+".tmeta.json")
}

func readJSON[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (b *XwalBackend) Meta(ariaID string) (*AriaMeta, error) {
	return readJSON[AriaMeta](b.metaPath(ariaID))
}
func (b *XwalBackend) SetMeta(ariaID string, meta *AriaMeta) error {
	if meta != nil && meta.LastActiveMS != 0 {
		b.store.Touch(ariaID, meta.LastActiveMS) // recency for `figaro list`
	}
	return writeJSON(b.metaPath(ariaID), meta)
}
func (b *XwalBackend) TranslationMeta(ariaID, providerName string) (*TranslationMeta, error) {
	return readJSON[TranslationMeta](b.tmetaPath(ariaID, providerName))
}
func (b *XwalBackend) SetTranslationMeta(ariaID, providerName string, meta *TranslationMeta) error {
	return writeJSON(b.tmetaPath(ariaID, providerName), meta)
}

// ---- live message (the single open/in-progress UI message per trunk) ----
//
// Committed messages live in the append-only xwal (the fig IR, which forks
// with the trunk); the one OPEN message is mutated as deltas stream, so it
// can't live in the segments. It's a plain r/w JSON blob, one per trunk, in
// the figaro data dir (root/_live/<id>.json). The store is dumb storage — the
// blob is opaque (the aria layer owns the livedoc encoding) and overwritten
// in place (last write wins) for optimistic updates. On restart a leftover
// blob is the caller's to discard or close.

func (b *XwalBackend) livePath(id string) string {
	return filepath.Join(b.root, "_live", id+".json")
}

// LiveBlob returns the persisted open-message blob for a trunk, or nil if
// there is none open.
func (b *XwalBackend) LiveBlob(ariaID string) ([]byte, error) {
	data, err := os.ReadFile(b.livePath(ariaID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// SetLiveBlob overwrites the open-message blob for a trunk (atomic
// tmp+rename), for optimistic in-place updates as deltas apply.
func (b *XwalBackend) SetLiveBlob(ariaID string, blob []byte) error {
	path := b.livePath(ariaID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearLive removes the open-message blob (on commit/close). A no-op if absent.
func (b *XwalBackend) ClearLive(ariaID string) error {
	err := os.Remove(b.livePath(ariaID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (b *XwalBackend) List() ([]AriaInfo, error) {
	out := []AriaInfo{}
	for _, n := range b.store.Nodes() {
		if n.Kind != string(kindConversation) {
			continue
		}
		info := AriaInfo{ID: n.ID, LastModified: time.UnixMilli(n.LastMS)}
		if m, _ := b.Meta(n.ID); m != nil {
			info.Meta = m
			info.MessageCount = m.MessageCount
		}
		// SINGLE SOURCE OF TRUTH: the count is the canonical conversational
		// message count of the live head's IR — not whatever a stale sidecar
		// (possibly written by an older binary with a different convention, or
		// before a heal) happens to hold. The head is now a single
		// deterministic leaf (figwal multi-head fix + heal), so this is
		// order-independent. Self-heal the sidecar when it disagrees.
		if c, ok := b.canonicalCount(n.ID); ok {
			info.MessageCount = c
			if info.Meta != nil && info.Meta.MessageCount != c {
				m := *info.Meta
				m.MessageCount = c
				_ = b.SetMeta(n.ID, &m)
				info.Meta = &m
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// canonicalCount recomputes a trunk's conversational message count from its
// live head IR via message.CountMessages — the single derivation every count
// path shares. Returns false if the head can't be opened (the sidecar value,
// if any, then stands).
func (b *XwalBackend) canonicalCount(id string) (int, bool) {
	lg, err := b.Open(id)
	if err != nil {
		return 0, false
	}
	entries := lg.Read()
	msgs := make([]message.Message, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, e.Payload)
	}
	return message.CountMessages(msgs), true
}

func (b *XwalBackend) Remove(ariaID string, recursive bool) error {
	b.dropHandle(ariaID)
	_ = os.Remove(b.metaPath(ariaID))
	_ = b.ClearLive(ariaID)
	return b.store.RemoveLeaf(ariaID, recursive)
}

func (b *XwalBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = map[string]*ariaHandle{}
	return nil
}
