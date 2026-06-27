package store

// XwalBackend implements store.Backend over the XwalStore aria tree.
// Each aria is opened once into a shared handle (the cache + close unit):
// the IR and per-provider translation channels are served as memoized
// read-through cachedLogs; the chalkboard is re-derived on demand via
// StateAt (no snapshot cache). It also exposes the tree operations
// (create loadout / conversation, fork, list) the daemon drives.

import (
	"encoding/json"
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
	xw    *xwal.XWAL
	ir    *cachedLog[message.Message]
	trans map[string]*cachedLog[[]json.RawMessage]

	// cbSnap memoizes the folded chalkboard snapshot against the
	// channel tail it was computed at; a read re-folds only when the
	// tail has moved (the watermark keeps StateAt cheap regardless).
	cbSnap chalkboard.Snapshot
	cbAt   uint64
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
// Caller holds b.mu.
func (b *XwalBackend) handleLocked(id string) (*ariaHandle, error) {
	if h := b.open[id]; h != nil {
		return h, nil
	}
	xw, err := b.store.OpenNode(id)
	if err != nil {
		return nil, err
	}
	h := &ariaHandle{
		xw:    xw,
		ir:    newCachedLog[message.Message](newXwalLog[message.Message](xw, chanIR, true)),
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
	defer b.mu.Unlock()
	h, err := b.handleLocked(ariaID)
	if err != nil {
		return nil, err
	}
	if c := h.trans[providerName]; c != nil {
		return c, nil
	}
	ch := transChannel(providerName)
	if !channelExists(h.xw, ch) {
		if err := h.xw.AddChannel(xwal.ChannelSpec{Name: ch, Kind: xwal.ChannelLog}); err != nil {
			return nil, err
		}
	}
	c := newCachedLog[[]json.RawMessage](newXwalLog[[]json.RawMessage](h.xw, ch, false))
	h.trans[providerName] = c
	return c, nil
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

// ChalkboardState folds the aria's chalkboard channel to current state,
// memoized against the channel tail (re-folds only when the tail moves).
func (b *XwalBackend) ChalkboardState(ariaID string) (chalkboard.Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, err := b.handleLocked(ariaID)
	if err != nil {
		return nil, err
	}
	last := channelLast(h.xw, chanChalkboard)
	if last == 0 {
		return chalkboard.Snapshot{}, nil
	}
	if h.cbSnap != nil && h.cbAt == last {
		return h.cbSnap.Clone(), nil
	}
	st, err := h.xw.StateAt(chanChalkboard, last)
	if err != nil {
		return nil, err
	}
	snap := chalkboard.Snapshot{}
	if err := json.Unmarshal(st, &snap); err != nil {
		return nil, err
	}
	h.cbSnap, h.cbAt = snap, last
	return snap.Clone(), nil
}

// ChalkboardPatches reads the whole chalkboard channel once and groups
// the (non-empty) patches by the IR LT they are keyed to.
func (b *XwalBackend) ChalkboardPatches(ariaID string) (map[uint64][]message.Patch, error) {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var first, last uint64
	for _, c := range h.xw.Channels() {
		if c.Name == chanChalkboard {
			first, last = c.First, c.Last
		}
	}
	out := map[uint64][]message.Patch{}
	for lt := first; lt >= 1 && lt <= last; lt++ {
		rec, err := h.xw.ReadAt(chanChalkboard, lt)
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
	return out, nil
}

// ApplyChalkboard appends a patch to the chalkboard channel, keyed to the
// next IR LT (a set records state for the turn about to happen).
func (b *XwalBackend) ApplyChalkboard(ariaID string, patch message.Patch) error {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return err
	}
	pb, _ := json.Marshal(patch)
	_, err = h.xw.Append(chanChalkboard, channelLast(h.xw, chanIR)+1, pb, nil)
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
	b.evict(ariaID) // it becomes frozen; drop any cached handle
	return b.store.Fork(ariaID)
}

func (b *XwalBackend) Node(id string) (NodeView, bool) { return b.store.Node(id) }
func (b *XwalBackend) Nodes() []NodeView               { return b.store.Nodes() }

func (b *XwalBackend) evict(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h := b.open[id]; h != nil {
		h.xw.Close()
		delete(b.open, id)
	}
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
	return writeJSON(b.metaPath(ariaID), meta)
}
func (b *XwalBackend) TranslationMeta(ariaID, providerName string) (*TranslationMeta, error) {
	return readJSON[TranslationMeta](b.tmetaPath(ariaID, providerName))
}
func (b *XwalBackend) SetTranslationMeta(ariaID, providerName string, meta *TranslationMeta) error {
	return writeJSON(b.tmetaPath(ariaID, providerName), meta)
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
		out = append(out, info)
	}
	return out, nil
}

func (b *XwalBackend) Remove(ariaID string) error {
	b.evict(ariaID)
	_ = os.Remove(b.metaPath(ariaID))
	return b.store.RemoveLeaf(ariaID)
}

func (b *XwalBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.open {
		h.xw.Close()
	}
	b.open = map[string]*ariaHandle{}
	return nil
}
