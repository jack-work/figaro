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

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

var _ Backend = (*XwalBackend)(nil)

type XwalBackend struct {
	root  string
	store *XwalStore
	mu    sync.Mutex
	open  map[string]*ariaHandle
	forms map[string]*Form
	metas map[string]*metaCache
	// labels is stump id -> what its birth record says it is. Never evicted
	// and never invalidated: a stump id is the hash of content that contains
	// the label, and a stump cannot be patched, so the mapping is a pure
	// function of the id.
	labels map[string]outfitLabel
	// touched is when each aria's caches were last used. These caches are
	// PURE COST once an aria has no agent: cachedLog decodes an entire IR
	// and every translation into the heap at construction, a Form holds
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
		forms:   map[string]*Form{},
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

// ---- form (re-derived via StateAt; mutation appends a patch) ----

func (b *XwalBackend) FormState(ariaID string) (form.Snapshot, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return form.Snapshot{}, err
	}
	snap, _ := f.Snapshot()
	return snap, nil
}

// form returns the aria's Form, opening (and replaying) it once.
func (b *XwalBackend) form(ariaID string) (*Form, error) {
	b.mu.Lock()
	b.touchLocked(ariaID)
	if f := b.forms[ariaID]; f != nil {
		b.mu.Unlock()
		return f, nil
	}
	b.mu.Unlock()

	// Replay outside the registry lock: it reads files, and holding the map
	// through that would serialize every aria behind one cold open.
	opened, err := OpenForm(&xwalFormLog{backend: b, ariaID: ariaID})
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing := b.forms[ariaID]; existing != nil {
		opened.Close() // lost the race; the winner owns the channel
		return existing, nil
	}
	b.forms[ariaID] = opened
	return opened, nil
}

// WatchForm registers a sink for every patch committed to an aria's form. It
// opens the Form if nobody had, which is what lets a listener follow a DORMANT
// aria: a form is a store object, and reading one does not need an agent.
func (b *XwalBackend) WatchForm(ariaID string, fn func(version uint64, patch message.Patch)) error {
	f, err := b.form(ariaID)
	if err != nil {
		return err
	}
	f.OnCommit(fn)
	return nil
}

// FormVersion is the durable index of the aria's last form patch.
func (b *XwalBackend) FormVersion(ariaID string) (uint64, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return 0, err
	}
	return f.Version(), nil
}

func (b *XwalBackend) FormPatches(ariaID string) ([]VersionedPatch, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return nil, err
	}
	return f.Patches(), nil
}

// ApplyForm appends a patch and returns its VERSION: the patch's own durable
// index in the form channel. The version is the number both an acknowledgement
// and a resume cursor need, and it is the append position, NOT an IR LT —
// several patches can arrive between two turns, so only the position tells
// them apart. It survives reopen because the channel is the durable truth.
func (b *XwalBackend) ApplyForm(ariaID string, patch message.Patch) (uint64, error) {
	return b.ApplyFormIf(ariaID, patch, 0)
}

// ApplyFormIf refuses the patch unless the form still stands at ifVersion.
func (b *XwalBackend) ApplyFormIf(ariaID string, patch message.Patch, ifVersion uint64) (uint64, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return 0, err
	}
	return f.Apply(patch, ifVersion)
}

// ---- tree operations (delegated) ----

// ForkWith is the one birth verb: see XwalStore.ForkWith. Every aria arrives
// this way — `fig new` forks the null root, `fig fork` forks an aria — and the
// patch it carries is its identity.
func (b *XwalBackend) ForkWith(parent string, atMainLT uint64, patch message.Patch) (string, uint64, error) {
	child, version, err := b.store.ForkWith(parent, atMainLT, patch)
	if err != nil {
		return "", 0, err
	}
	// The Form for a node born a moment ago must not be a replay of a channel
	// that was empty when someone else opened it first.
	b.mu.Lock()
	delete(b.forms, child)
	b.mu.Unlock()
	return child, version, nil
}

// CreateForm mints an unbound form: see XwalStore.CreateForm. `fig form
// new` forks the null root; `fig form fork` duplicates a form. Same
// forms-cache hygiene as ForkWith, same reason.
func (b *XwalBackend) CreateForm(parent string, patch message.Patch) (string, uint64, error) {
	id, version, err := b.store.CreateForm(parent, patch)
	if err != nil {
		return "", 0, err
	}
	b.mu.Lock()
	delete(b.forms, id)
	b.mu.Unlock()
	return id, version, nil
}

func (b *XwalBackend) CreateOutfit(name string, patch message.Patch) (string, error) {
	return b.store.CreateOutfit(name, patch)
}

// KeepStump names the one stump collection spares. WHICH stump that is, is
// policy — the angelus knows what the configured default resolves to today; the
// store only knows what it was asked to build.
func (b *XwalBackend) KeepStump(id string) { b.store.KeepStump(id) }
func (b *XwalBackend) CreateConversation(outfitID string) (string, error) {
	return b.store.CreateConversation(outfitID)
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
	return OwnerInfo{Trunk: o.Trunk, Outfit: o.Stump, IsRoot: o.IsRoot}, nil
}

// A node's OUTFIT is resolved here rather than in the store: the store knows
// ids and the topology, the backend knows how to read a node. The stump each
// node was born under comes from the topology walk; its label comes from its
// own birth record, once.
func (b *XwalBackend) Node(id string) (NodeView, bool) {
	n, ok := b.store.Node(id)
	if ok {
		b.label(&n)
	}
	return n, ok
}

func (b *XwalBackend) Nodes() []NodeView         { return b.labelAll(b.store.Nodes()) }
func (b *XwalBackend) Conversations() []NodeView { return b.labelAll(b.store.Conversations()) }
func (b *XwalBackend) ConversationIDs() []string { return b.store.ConversationIDs() }

// outfitLabel is a stump's own account of itself.
type outfitLabel struct{ name, version string }

// labelAll resolves each DISTINCT stump once and fills the rest from that.
// Per-node locking here cost more than the read it was protecting: a listing
// is thousands of nodes over a handful of outfits.
func (b *XwalBackend) labelAll(nodes []NodeView) []NodeView {
	// Two shapes in one listing: an aria under a stump takes the stump's label
	// (memoized per stump — a listing of 200 arias on four outfits reads four
	// birth records), and an aria born of ForkWith states its own on its form.
	var seen map[string]outfitLabel
	for i := range nodes {
		stump := nodes[i].Stump
		if stump == "" {
			l := b.labelOf(&nodes[i])
			nodes[i].Outfit, nodes[i].Version = l.name, l.version
			continue
		}
		l, ok := seen[stump]
		if !ok {
			l = b.stumpLabel(stump)
			if seen == nil {
				seen = map[string]outfitLabel{}
			}
			seen[stump] = l
		}
		nodes[i].Outfit, nodes[i].Version = l.name, l.version
	}
	return nodes
}

func (b *XwalBackend) label(n *NodeView) {
	l := b.labelOf(n)
	n.Outfit, n.Version = l.name, l.version
}

// labelOf answers what an aria is wearing, from the one place that states it.
//
// An aria born since ForkWith says so on its OWN form: the birth patch carries
// system.outfit_name and the content hash of itself. An aria born before that
// hangs under a stump, and the stump's birth record is where the name lives —
// read once and memoized, which immutability licenses.
//
// Two shapes, and no migration between them: re-parenting an old aria would mean
// copying its inherited prefix into its own channel, renumbering its form
// versions, and every IR record's cursor stamp would then point at the wrong
// patch. The old shape reads fine; it just stops being minted.
func (b *XwalBackend) labelOf(n *NodeView) outfitLabel {
	if n.Stump != "" {
		return b.stumpLabel(n.Stump)
	}
	snap, err := b.FormState(n.ID)
	if err != nil {
		return outfitLabel{}
	}
	l := outfitLabel{name: snapString(snap, keyOutfitName), version: snapString(snap, keyOutfitVer)}
	if l.name == "" {
		l.name, l.version = snapString(snap, keyLegacyName), snapString(snap, keyLegacyVer)
	}
	return l
}

// stumpLabel reads a stump's name and version out of the birth patch it wrote,
// which is the only place either has ever been stated authoritatively -- the id
// used to restate the name, and a form key of the same name is the
// agent's mutable copy. Memoized for the life of the process: a stump id is the
// hash of content that contains the label, and a stump cannot be patched, so
// id -> label is a pure function.
func (b *XwalBackend) stumpLabel(id string) outfitLabel {
	b.mu.Lock()
	l, ok := b.labels[id]
	b.mu.Unlock()
	if ok {
		return l
	}
	if snap, err := b.FormState(id); err == nil {
		l = outfitLabel{name: snapString(snap, keyOutfitName), version: snapString(snap, keyOutfitVer)}
		if l.name == "" {
			l.name, l.version = snapString(snap, keyLegacyName), snapString(snap, keyLegacyVer)
		}
		// Only a successful read is memoized. The licence for the memo is that
		// a stump's CONTENT cannot change, which says nothing about an error:
		// caching one blanked every row for that outfit for the life of the
		// daemon, and nothing ever invalidated it.
		b.mu.Lock()
		if b.labels == nil {
			b.labels = map[string]outfitLabel{}
		}
		b.labels[id] = l
		b.mu.Unlock()
	}
	return l
}

func snapString(s form.Snapshot, key string) string {
	raw, ok := s.Get(key)
	if !ok {
		return ""
	}
	var out string
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out
}

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
			if _, hasForm := b.forms[id]; !hasForm {
				if _, hasMeta := b.metas[id]; !hasMeta {
					delete(b.touched, id)
					continue
				}
			}
		}
		delete(b.open, id)
		if f := b.forms[id]; f != nil {
			f.Close()
		}
		delete(b.forms, id)
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
	if f := b.forms[ariaID]; f != nil {
		f.Close()
	}
	delete(b.forms, ariaID)
	delete(b.metas, ariaID)
	b.mu.Unlock()
	_ = os.Remove(b.metaPath(ariaID))
	return b.store.RemoveLeaf(ariaID, recursive)
}

// CollectStump removes a childless outfit stump. See XwalStore.CollectStump.
func (b *XwalBackend) CollectStump(stumpID string) error {
	return b.store.CollectStump(stumpID)
}

func (b *XwalBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = map[string]*ariaHandle{}
	for _, f := range b.forms {
		f.Close()
	}
	b.forms = map[string]*Form{}
	b.metas = map[string]*metaCache{}
	return b.store.trunks.Close() // Trunks.Close flushes the topology index
}

// LastTS delegates node recency to figwal: see XwalStore.LastTS.
func (b *XwalBackend) LastTS(id string) int64 { return b.store.LastTS(id) }
