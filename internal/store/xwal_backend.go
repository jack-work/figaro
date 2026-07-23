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
	"os"
	"path/filepath"
	"sync"

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
	chalk map[string]*chalkCache
	metas map[string]*metaCache
}

type ariaHandle struct {
	ir    *cachedLog[message.Message]
	trans map[string]*cachedLog[[]json.RawMessage]
}

type chalkCache struct {
	mu      sync.Mutex
	ready   bool
	state   chalkboard.Snapshot
	patches map[uint64][]message.Patch
}

type metaCache struct {
	mu     sync.Mutex
	loaded bool
	value  *AriaMeta
}

func NewXwalBackend(root string) (*XwalBackend, error) {
	st, err := OpenXwalStore(root)
	if err != nil {
		return nil, err
	}
	return &XwalBackend{
		root:  root,
		store: st,
		open:  map[string]*ariaHandle{},
		chalk: map[string]*chalkCache{},
		metas: map[string]*metaCache{},
	}, nil
}

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

func transChannel(provider string) string { return "translations-v2/" + provider }

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
	if err := b.store.ensureOpaqueTranslationChannel("translations/"+providerName, ch); err != nil {
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

func (b *XwalBackend) SyncTranslation(ariaID, providerName string) error {
	return b.store.trunks.SyncChannel(ariaID, transChannel(providerName))
}

func (b *XwalBackend) ensureChannel(spec xwal.ChannelSpec) error {
	return b.store.ensureChannel(spec)
}

// ---- chalkboard (re-derived via StateAt; mutation appends a patch) ----

func (b *XwalBackend) ChalkboardState(ariaID string) (chalkboard.Snapshot, error) {
	c := b.chalkCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := b.loadChalkboardLocked(ariaID, c); err != nil {
		return nil, err
	}
	return c.state.Clone(), nil
}

func (b *XwalBackend) chalkCache(ariaID string) *chalkCache {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.chalk[ariaID]
	if c == nil {
		c = &chalkCache{}
		b.chalk[ariaID] = c
	}
	return c
}

func (b *XwalBackend) loadChalkboardLocked(ariaID string, c *chalkCache) error {
	if c.ready {
		return nil
	}
	xw, err := b.store.OpenNode(ariaID)
	if err != nil {
		return err
	}
	defer xw.Close()
	var first, last uint64
	for _, ch := range xw.Channels() {
		if ch.Name == chanChalkboard {
			first, last = ch.First, ch.Last
			break
		}
	}
	if first == 0 && last > 0 {
		first = 1
	}
	state := chalkboard.Snapshot{}
	patches := map[uint64][]message.Patch{}
	for lt := first; lt >= 1 && lt <= last; lt++ {
		rec, err := xw.ReadAt(chanChalkboard, lt)
		if err != nil {
			return err
		}
		var p message.Patch
		if err := json.Unmarshal(rec.Payload, &p); err != nil {
			return err
		}
		for k, v := range p.Set {
			state[k] = v
		}
		for _, k := range p.Remove {
			delete(state, k)
		}
		if !p.IsEmpty() {
			patches[rec.MainLT] = append(patches[rec.MainLT], p)
		}
	}
	c.state = state
	c.patches = patches
	c.ready = true
	return nil
}

func (b *XwalBackend) ChalkboardPatches(ariaID string) (map[uint64][]message.Patch, error) {
	c := b.chalkCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := b.loadChalkboardLocked(ariaID, c); err != nil {
		return nil, err
	}
	out := make(map[uint64][]message.Patch, len(c.patches))
	for lt, ps := range c.patches {
		out[lt] = append([]message.Patch(nil), ps...)
	}
	return out, nil
}

// ApplyChalkboard appends a patch to the chalkboard channel, keyed to the
// next IR LT (a set records state for the turn about to happen). Routes
// through Trunks.AppendChannel so it serializes with Fork/Promote.
func (b *XwalBackend) ApplyChalkboard(ariaID string, patch message.Patch) error {
	pb, _ := json.Marshal(patch)
	c := b.chalkCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	var mainLT uint64
	if c.ready {
		b.mu.Lock()
		h := b.open[ariaID]
		b.mu.Unlock()
		if h != nil {
			if tail, ok := h.ir.PeekTail(); ok {
				mainLT = tail.LT + 1
			} else {
				mainLT = 1
			}
		}
	}
	// Pass mainLT=0 to let Trunks compute (mainTail+1) internally; the
	// chalkboard channel is reducible/keyed-forward and the default
	// "one ahead" semantics match the previous channelLast+1 behavior.
	_, err := b.store.trunks.AppendChannel(ariaID, chanChalkboard, 0, pb, nil)
	if err != nil {
		return err
	}
	if c.ready && mainLT > 0 {
		c.state = c.state.Apply(patch)
		if !patch.IsEmpty() {
			c.patches[mainLT] = append(c.patches[mainLT], patch)
		}
	} else {
		c.ready = false
	}
	return nil
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
func (b *XwalBackend) Conversations() []NodeView       { return b.store.Conversations() }
func (b *XwalBackend) ConversationIDs() []string       { return b.store.ConversationIDs() }

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
	c := b.metaCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		value, err := readJSON[AriaMeta](b.metaPath(ariaID))
		if err != nil {
			return nil, err
		}
		c.value = value
		c.loaded = true
	}
	if c.value == nil {
		return nil, nil
	}
	value := *c.value
	return &value, nil
}
func (b *XwalBackend) SetMeta(ariaID string, meta *AriaMeta) error {
	c := b.metaCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := writeJSON(b.metaPath(ariaID), meta); err != nil {
		return err
	}
	c.loaded = true
	if meta == nil {
		c.value = nil
	} else {
		value := *meta
		c.value = &value
	}
	return nil
}

func (b *XwalBackend) metaCache(ariaID string) *metaCache {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.metas[ariaID]
	if c == nil {
		c = &metaCache{}
		b.metas[ariaID] = c
	}
	return c
}

func (b *XwalBackend) Remove(ariaID string, recursive bool) error {
	b.dropHandle(ariaID)
	b.mu.Lock()
	delete(b.chalk, ariaID)
	delete(b.metas, ariaID)
	b.mu.Unlock()
	_ = os.Remove(b.metaPath(ariaID))
	return b.store.RemoveLeaf(ariaID, recursive)
}

func (b *XwalBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = map[string]*ariaHandle{}
	b.chalk = map[string]*chalkCache{}
	b.metas = map[string]*metaCache{}
	return b.store.trunks.Close()
}
