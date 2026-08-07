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
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

var _ Backend = (*XwalBackend)(nil)

type XwalBackend struct {
	root  string
	store *XwalStore
	mu    sync.Mutex
	open  map[string]*ariaHandle
	chalk map[string]*chalkCache
	metas map[string]*metaCache
	// touched is when each aria's caches were last used. These caches are
	// PURE COST once an aria has no agent: cachedLog decodes an entire IR
	// and every translation into the heap at construction, chalkCache holds
	// the whole board and every patch, and nothing but Remove ever deleted
	// an entry. Measured on a real daemon: 209 arias resident, 107,439
	// messages, 3.0 GB private -- against 424 MB of IR on disk, because Go
	// structs run 3-5x the encoded bytes.
	//
	// Every one of them is rebuildable from the store, so residency is a
	// cache decision and wants a cache's discipline.
	touched map[string]time.Time

	// irWindow bounds resident decoded IR entries per aria. 0 retains
	// everything. Translation caches are deliberately NOT windowed: their
	// payload is the request body, so the wire form must be complete, and
	// measured they are a fraction of the decoded IR anyway.
	irWindow int
	// irBudget bounds resident decoded IR in bytes. It is the knob that
	// actually controls memory; see cachedLog.budget.
	irBudget int
}

// irEntrySize estimates one IR entry's retained bytes as its encoded size
// times a measured inflation factor.
//
// Deriving it from the decoded struct instead was tried and abandoned: it
// means guessing allocator rounding for every string, slice and boxed
// map[string]interface{} value, and the attempt came out 3x low (4.1 MiB
// estimated against 12.1 MiB measured on a real aria). The encoded size is
// known for free at decode, and the ratio between the two is stable — 4.0x and
// 5.3x on two real arias — so one constant beats a model.
//
// An entry appended this process has no encoded size recorded by the caller;
// fall back to the content bytes, which is the right order of magnitude and
// self-corrects on the next restore.
func irEntrySize(e Entry[message.Message]) int {
	if e.EncodedBytes > 0 {
		return e.EncodedBytes * irDecodeInflation
	}
	n := 0
	for _, c := range e.Payload.Content {
		n += len(c.Text) + len(c.Data)
	}
	return n * irDecodeInflation
}

// irDecodeInflation is how much larger decoded IR is than its wire bytes.
// Measured, not assumed: 4.0x on a 2556-message aria and 5.3x on a
// 1760-message one. The higher of the two, so a budget under-holds rather than
// over-holds — being wrong toward less memory is the safe direction here.
const irDecodeInflation = 5

type ariaHandle struct {
	ir    *cachedLog[message.Message]
	trans map[string]*cachedLog[[]json.RawMessage]
}

type chalkCache struct {
	mu    sync.Mutex
	ready bool
	state chalkboard.Snapshot
	// patches in version order. No grouping by turn: an IR entry carries
	// the board version at its turn, so the reader walks both in step.
	patches []VersionedPatch
}

type metaCache struct {
	mu     sync.Mutex
	loaded bool
	value  *AriaMeta
}

// NewXwalBackend opens the aria tree at root. segmentSize <= 0 takes the
// configured default; the daemon passes config's, tests pass nothing.
//
// The presentation hierarchy defaults to the topology. A build with the
// trunk capability replaces it via Store().SetTree; see internal/figaro/wire.
func NewXwalBackend(root string, segmentSize int) (*XwalBackend, error) {
	st, err := OpenXwalStore(root, segmentSize)
	if err != nil {
		return nil, err
	}
	return &XwalBackend{
		root:    root,
		store:   st,
		open:    map[string]*ariaHandle{},
		chalk:   map[string]*chalkCache{},
		metas:   map[string]*metaCache{},
		touched: map[string]time.Time{},
	}, nil
}

// Normalize runs deferred topology work now. See XwalStore.Normalize.
func (b *XwalBackend) Normalize() (int, error) { return b.store.Normalize() }

// Store is the underlying aria store, for wiring that installs optional
// capabilities.
func (b *XwalBackend) Store() *XwalStore { return b.store }

// handleLocked returns the shared handle for an aria, opening it once.
// Caller holds b.mu. The handle carries the row caches for the aria's
// channels; nothing else. Fresh *xwal.XWAL instances are opened on
// demand by the xwalLog inside each cachedLog.
func (b *XwalBackend) handleLocked(id string) (*ariaHandle, error) {
	b.touchLocked(id)
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
		ir: newWindowedLog[message.Message](
			newXwalLog[message.Message](b.store, id, chanIR, true),
			b.irWindow, b.irBudget, irDecodeInflation, irEntrySize),
		trans: map[string]*cachedLog[[]json.RawMessage]{},
	}
	b.open[id] = h
	return h, nil
}

func (b *XwalBackend) Open(ariaID string) (Log[message.Message], error) {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Reading the content is where a trailing sidecar gets caught up; see
	// meta_heal.go. No-op unless the watermark lags the tail.
	b.healMeta(ariaID, h.ir)
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

func (b *XwalBackend) Kick() { b.store.trunks.Kick() }

// ---- chalkboard (re-derived via StateAt; mutation appends a patch) ----

func (b *XwalBackend) ChalkboardState(ariaID string) (chalkboard.Snapshot, error) {
	c := b.chalkCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := b.loadChalkboardLocked(ariaID, c); err != nil {
		return chalkboard.Snapshot{}, err
	}
	return c.state.Clone(), nil
}

func (b *XwalBackend) chalkCache(ariaID string) *chalkCache {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.touchLocked(ariaID)
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
	var patches []VersionedPatch
	for lt := first; lt >= 1 && lt <= last; lt++ {
		rec, err := xw.ReadAt(chanChalkboard, lt)
		if err != nil {
			return err
		}
		var p message.Patch
		if err := json.Unmarshal(rec.Payload, &p); err != nil {
			return err
		}
		state = state.Apply(p)
		if !p.IsEmpty() {
			patches = append(patches, VersionedPatch{Version: lt, Patch: p})
		}
	}
	c.state = state
	c.patches = patches
	c.ready = true
	return nil
}

func (b *XwalBackend) ChalkboardPatches(ariaID string) ([]VersionedPatch, error) {
	c := b.chalkCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := b.loadChalkboardLocked(ariaID, c); err != nil {
		return nil, err
	}
	return append([]VersionedPatch(nil), c.patches...), nil
}

// ApplyChalkboard appends a patch and returns its VERSION: the patch's own
// durable index in the chalkboard channel.
//
// It does not read the timeline. The channel is unkeyed, so a patch is
// written with no reference to the turn in flight and nothing to serialize
// against -- which is what lets a `set` land mid-turn instead of waiting
// for the round to end.
//
// The version is the number both an acknowledgement and a resume cursor
// need, and it is the append position, NOT an IR LT: several patches can
// arrive between two turns, so only the position tells them apart. It
// survives reopen because the channel is the durable truth.
//
// Durability precedes visibility: the in-memory board advances only after
// the append returns, so a failure leaves the published board and the log
// agreeing rather than diverging.
func (b *XwalBackend) ApplyChalkboard(ariaID string, patch message.Patch) (uint64, error) {
	pb, _ := json.Marshal(patch)
	c := b.chalkCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Store.Append, not Trunks.AppendChannel: the poison gate and the
	// dirty/touch bookkeeping must see chalkboard writes.
	version, err := b.store.trunks.Append(ariaID, chanChalkboard, 0, pb, nil)
	if err != nil {
		return 0, err
	}
	if c.ready {
		c.state = c.state.Apply(patch)
		if !patch.IsEmpty() {
			c.patches = append(c.patches, VersionedPatch{Version: version, Patch: patch})
		}
	}
	return version, nil
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

func (b *XwalBackend) touchLocked(id string) {
	if b.touched != nil {
		b.touched[id] = time.Now()
	}
}

// EvictIdle drops the cached IR, translations, board and metadata of every
// aria that is NOT live and has not been touched for idle. It returns how
// many it released.
//
// Two rules, and the first is not negotiable. An aria with a LIVE AGENT is
// never evicted: cachedLog is shared per (aria, channel) precisely so a
// reader sees the writer's appends, and dropping it mid-life would hand the
// next reader a second instance built from disk while the agent still holds
// the first. Everything here is rebuildable, but rebuildable is not the same
// as interchangeable while someone is writing.
//
// The second is that eviction is a CACHE decision, so it costs only the next
// read. The store below has always had this discipline (xwal.Store unloads an
// idle lineage's head); this layer simply never participated in it, which is
// the whole of the leak.
// SetIRWindow sets the resident decoded-IR cap for handles opened from here
// on. Existing handles keep their window: changing it under a live agent
// mid-turn is not worth the coordination, and a restart applies it everywhere.
func (b *XwalBackend) SetIRWindow(n int) {
	b.mu.Lock()
	b.irWindow = n
	b.mu.Unlock()
}

// SetIRBudget sets the resident decoded-IR byte budget for handles opened from
// here on. Same "new handles only" rule as SetIRWindow.
func (b *XwalBackend) SetIRBudget(n int) {
	b.mu.Lock()
	b.irBudget = n
	b.mu.Unlock()
}

// TrimResident trims every non-live aria's IR window to keep entries and
// reports how many rows were released. It is the reaper's control surface: the
// caller decides WHEN, this decides how.
//
// Live arias are skipped for the same reason EvictIdle skips them — one
// cachedLog is shared between the writing agent and concurrent readers, and a
// window is only safe to shrink when nobody is mid-fold across it.
func (b *XwalBackend) TrimResident(live map[string]bool, keep int) int {
	b.mu.Lock()
	handles := make([]*ariaHandle, 0, len(b.open))
	for id, h := range b.open {
		if !live[id] {
			handles = append(handles, h)
		}
	}
	b.mu.Unlock()

	released := 0
	for _, h := range handles {
		if h.ir != nil {
			released += h.ir.Trim(keep)
		}
	}
	return released
}

// ResidentRows reports resident decoded IR entries across every open aria —
// the number a window bounds, as opposed to Resident's aria count.
func (b *XwalBackend) ResidentRows() int {
	n, _ := b.residency()
	return n
}

// ResidentIRBytes is the estimated retained size of every open aria's IR
// window. This is the number to watch: rows are a poor proxy, because the
// large entries cluster at the tail.
func (b *XwalBackend) ResidentIRBytes() int {
	_, n := b.residency()
	return n
}

func (b *XwalBackend) residency() (rows, bytes int) {
	b.mu.Lock()
	handles := make([]*ariaHandle, 0, len(b.open))
	for _, h := range b.open {
		handles = append(handles, h)
	}
	b.mu.Unlock()

	for _, h := range handles {
		if h.ir != nil {
			rows += h.ir.Resident()
			bytes += h.ir.ResidentBytes()
		}
	}
	return rows, bytes
}

func (b *XwalBackend) EvictIdle(live map[string]bool, idle time.Duration) int {
	cutoff := time.Now().Add(-idle)
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for id := range b.touched {
		if live[id] || b.touched[id].After(cutoff) {
			continue
		}
		if _, held := b.open[id]; !held {
			if _, hasChalk := b.chalk[id]; !hasChalk {
				if _, hasMeta := b.metas[id]; !hasMeta {
					delete(b.touched, id)
					continue
				}
			}
		}
		delete(b.open, id)
		delete(b.chalk, id)
		delete(b.metas, id)
		delete(b.touched, id)
		n++
	}
	return n
}

// Resident reports how many arias hold cached state, for the daemon to log
// and for a test to assert on without reaching into the maps.
func (b *XwalBackend) Resident() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.open)
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
	if err := b.loadMetaLocked(ariaID, c); err != nil {
		return nil, err
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

// loadMetaLocked fills the cache from the sidecar once. Caller holds c.mu.
func (b *XwalBackend) loadMetaLocked(ariaID string, c *metaCache) error {
	if c.loaded {
		return nil
	}
	value, err := readJSON[AriaMeta](b.metaPath(ariaID))
	if err != nil {
		return err
	}
	c.value = value
	c.loaded = true
	return nil
}

func (b *XwalBackend) metaCache(ariaID string) *metaCache {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.touchLocked(ariaID)
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
	return b.store.trunks.Close() // Trunks.Close flushes the topology index
}
