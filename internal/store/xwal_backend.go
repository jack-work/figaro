package store

// XwalBackend memoizes one log per (aria, channel) and never caches
// *xwal.XWAL handles: every read and write opens a fresh one through trunks,
// which serialize against Fork/Promote inside figwal. A Fork does not
// invalidate the row cache (bytes on disk are append-only) and a Promote is
// cosmetic.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

var _ Backend = (*XwalBackend)(nil)

type XwalBackend struct {
	root  string
	store *XwalStore
	moves *Moves
	mu    sync.Mutex
	open  map[string]*ariaHandle
	forms map[string]*Form
	metas map[string]*metaCache

	modelsMu sync.Mutex
	models   *Form
	// librettos: one fold goroutine per libretto, not per observer.
	librettos map[string]*Libretto
	// lastTS is aria id -> newest record timestamp, memoized.
	lastTS map[string]int64

	// ttl holds only nodes carrying system.ttl; nil until the first read seeds
	// it from the sidecars. Its own lock: the sweep reads it on a ticker and
	// must not queue behind a cold node open holding b.mu.
	ttlMu    sync.Mutex
	ttl      map[string]TTLEntry
	access   map[string]int64
	reaped   map[string]ReapedEntry
	onReaped []func()
	encoders translatorEncoders

	// labels is never invalidated for a stump: its id is the hash of content
	// containing the label, and a stump cannot be patched.
	labels map[string]outfitLabel
	// touched is when each aria's caches were last used; they are pure cost
	// once an aria has no agent.
	touched map[string]time.Time

	// irWindow: 0 retains everything. Translation caches are not windowed:
	// their payload is the request body, so the wire form must be complete.
	irWindow int
	irBudget int
	// irTree is the decoded-IR residency shared by every aria: one budget, one
	// eviction order; a child's prefix lives in its ancestor's node.
	irTree  *fwtree.Budget
	irCache *fwtree.Cache[Entry[message.Message]]

	// transTree is the translations' shared budget; one cache per provider,
	// each with a node per aria.
	transTree   *fwtree.Budget
	transCaches map[string]*fwtree.Cache[Entry[[]json.RawMessage]]

	// transBudget: the payload is the provider's wire form, so the estimate is
	// the bytes themselves.
	transBudget int
}

// Residency defaults: decoded-estimate budgets. irWindow stays 0, so bytes
// bind rather than an accidental row count.
const (
	DefaultIRBudgetBytes          = 1 << 20
	DefaultTranslationBudgetBytes = 4 << 20

	// DefaultIRTreeBytes is for the whole process, not per aria: any single
	// aria may hold far more than the per-aria figure, all of them together may not.
	DefaultIRTreeBytes = 32 << 20

	// DefaultTransTreeBytes: the same shape for translations.
	DefaultTransTreeBytes = 32 << 20
)

// irEntrySize estimates retained bytes as encoded size times decode inflation.
func irEntrySize(e Entry[message.Message]) int {
	if e.EncodedBytes > 0 {
		return e.EncodedBytes * irDecodeNum / irDecodeDenom
	}
	n := 0
	for _, c := range e.Payload.Content {
		n += len(c.Text) + len(c.Data)
	}
	return n * irDecodeNum / irDecodeDenom
}

// Decoded IR runs 27/20 of its wire bytes.
const (
	irDecodeNum   = 27 // 27/20 = 1.35
	irDecodeDenom = 20
)

// transEntrySize: the payload is the provider's wire form, so its encoded
// size is the bytes themselves plus the []json.RawMessage headers.
func transEntrySize(e Entry[[]json.RawMessage]) int {
	n := 0
	for _, raw := range e.Payload {
		n += len(raw) + 16
	}
	return n
}

type ariaHandle struct {
	ir    *treeLog[message.Message]
	trans map[string]*treeLog[[]json.RawMessage]
}

type metaCache struct {
	mu    sync.Mutex // writers only: file write then publish, in that order
	state atomic.Pointer[metaState]
}

// metaState is immutable once published. A nil Value means the sidecar is
// absent, which is different from not having looked.
type metaState struct{ Value *AriaMeta }

// loadOnce publishes the sidecar with no lock held: a reader racing a SetMeta
// loses the CAS, so a file read taken before a write can never overwrite the
// value that write published.
func (c *metaCache) loadOnce(path string) (*metaState, error) {
	if st := c.state.Load(); st != nil {
		return st, nil
	}
	value, err := readJSON[AriaMeta](path)
	if err != nil {
		return nil, err
	}
	fresh := &metaState{Value: value}
	if !c.state.CompareAndSwap(nil, fresh) {
		return c.state.Load(), nil
	}
	return fresh, nil
}

// segmentSize <= 0 takes the configured default.
func NewXwalBackend(root string, segmentSize int) (*XwalBackend, error) {
	st, err := OpenXwalStore(root, segmentSize)
	if err != nil {
		return nil, err
	}
	moves, err := OpenMoves(root)
	if err != nil {
		st.Close()
		return nil, err
	}
	b := &XwalBackend{
		root:        root,
		store:       st,
		moves:       moves,
		open:        map[string]*ariaHandle{},
		forms:       map[string]*Form{},
		metas:       map[string]*metaCache{},
		touched:     map[string]time.Time{},
		irBudget:    DefaultIRBudgetBytes,
		irTree:      fwtree.NewBudget(DefaultIRTreeBytes),
		transTree:   fwtree.NewBudget(DefaultTransTreeBytes),
		transCaches: map[string]*fwtree.Cache[Entry[[]json.RawMessage]]{},
		transBudget: DefaultTranslationBudgetBytes,
	}
	// One cache, a node per aria: that is what makes a fork's prefix its
	// ancestor's runs rather than a copy.
	b.irCache = NewIRCache[message.Message](b.irTree, func(node string) Log[message.Message] {
		return newXwalLog[message.Message](b.store, node, chanIR, true)
	}, irEntrySize, irKey[message.Message])
	return b, nil
}

// Normalize runs deferred topology work now.
func (b *XwalBackend) Normalize() (int, error) { return b.store.Normalize() }

func (b *XwalBackend) Store() *XwalStore { return b.store }

// handleLocked opens the shared handle once. Caller holds b.mu.
func (b *XwalBackend) handleLocked(id string) (*ariaHandle, error) {
	b.touchLocked(id)
	if h := b.open[id]; h != nil {
		return h, nil
	}
	// Sanity-open once to fail fast on an unknown aria.
	start := time.Now()
	xw, err := b.store.openNode(id)
	if err != nil {
		return nil, err
	}
	_ = xw.Close()
	h := &ariaHandle{
		ir: newTreeLog[message.Message](
			newXwalLog[message.Message](b.store, id, chanIR, true),
			id, b.irCache, irEntrySize, irKey[message.Message],
			func() []fwtree.Ref { return b.store.Lineage(id) }).
			withNodeOpener(func(node string) Log[message.Message] {
				return newXwalLog[message.Message](b.store, node, chanIR, true)
			}).seedingTail(),
		trans: map[string]*treeLog[[]json.RawMessage]{},
	}
	b.open[id] = h
	figOtel.RecordStoreOpen(context.Background(), time.Since(start))
	return h, nil
}

// OpenNode reads one node's IR: an ancestor's frozen index node included,
// which is not an aria and must not get an aria handle, a sidecar or the IR
// door.
func (b *XwalBackend) OpenNode(node string) Log[message.Message] {
	return newXwalLog[message.Message](b.store, node, chanIR, true)
}

func (b *XwalBackend) OpenFigIR(ariaID string) (Log[message.Message], error) {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Reading the content is where a trailing sidecar is caught up.
	b.healMeta(ariaID, h.ir)
	return &figIRLog{Log: h.ir, backend: b, ariaID: ariaID}, nil
}

func (b *XwalBackend) CloseOpenToolCalls(ariaID string) (int, error) {
	lg, err := b.OpenFigIR(ariaID)
	if err != nil {
		return 0, err
	}
	guard, ok := lg.(*figIRLog)
	if !ok {
		return 0, fmt.Errorf("store: aria %s IR log is not guarded", ariaID)
	}
	return guard.CloseOpenToolCalls()
}

const translationPrefix = "translations-v2/"

func transChannel(provider string) string { return translationPrefix + provider }

func (b *XwalBackend) OpenTranslator(ariaID, providerName string) (Log[[]json.RawMessage], error) {
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
	cache := b.transCacheFor(providerName)
	b.mu.Unlock()
	ch := transChannel(providerName)
	c := newTreeLog[[]json.RawMessage](
		newXwalLog[[]json.RawMessage](b.store, ariaID, ch, false),
		ariaID, cache, transEntrySize, transKey[[]json.RawMessage],
		func() []fwtree.Ref { return b.store.Lineage(ariaID) }).
		// A coordinate holds exactly one entry (the channel is addressed by its
		// own LT), so the writer's append can be published rather than fetched back.
		seedingTail().
		withNodeOpener(func(node string) Log[[]json.RawMessage] {
			return newXwalLog[[]json.RawMessage](b.store, node, ch, false)
		})
	b.mu.Lock()
	if existing := h.trans[providerName]; existing != nil {
		b.mu.Unlock()
		return existing, nil
	}
	h.trans[providerName] = c
	b.mu.Unlock()
	return c, nil
}

func (b *XwalBackend) FormState(ariaID string) (form.Snapshot, error) {
	at, err := b.FormAt(ariaID)
	return at.Snapshot, err
}

// FormAt answers both from one atomic load; asking twice can pair a state
// from before a write with a version from after it.
func (b *XwalBackend) FormAt(ariaID string) (FormAt, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return FormAt{}, err
	}
	return f.Read(), nil
}

func (b *XwalBackend) FormSince(ariaID string, since uint64) (FormSince, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return FormSince{}, err
	}
	return f.Since(since), nil
}

func (b *XwalBackend) form(ariaID string) (*Form, error) {
	// A libretto is not a node: its Form lives in the libretto registry and is
	// the writer the fold appends through; a second Form over the same stump
	// would freeze at the version it opened at.
	if source, ok := SourceOfLibretto(ariaID); ok {
		return nil, fmt.Errorf(
			"form %s: a libretto must be read through Libretto(%q), not as a node", ariaID, source)
	}
	b.mu.Lock()
	b.seenLocked(ariaID)
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
	// Registered where the form is opened, so every writer passes it.
	opened.OnCommit(func(_ uint64, patch message.Patch) {
		b.wroteTo(ariaID, time.Now().UnixMilli())
		if namesOutfit(patch) {
			b.labelChanged(ariaID)
			b.mirrorFormLifetime(ariaID, opened.Read().Snapshot)
			return
		}
		b.ttlCommitted(ariaID, patch)
	})

	b.mu.Lock()
	defer b.mu.Unlock()
	if existing := b.forms[ariaID]; existing != nil {
		opened.Close() // lost the race; the winner owns the channel
		return existing, nil
	}
	b.forms[ariaID] = opened
	return opened, nil
}

func namesOutfit(patch message.Patch) bool {
	for _, k := range []string{keyOutfitName, keyOutfitVer, keyLegacyName, keyLegacyVer} {
		if _, ok := patch.Entry(k); ok {
			return true
		}
		for _, ent := range patch.Entries() {
			if !ent.IsRemoval() {
				continue
			}
			r := ent.Key
			if r == k {
				return true
			}
		}
	}
	return false
}

// WatchForm opens the Form if nobody had, so a listener can follow a dormant
// aria without an agent.
func (b *XwalBackend) WatchForm(ariaID string, fn func(version uint64, patch message.Patch)) error {
	b.touch(ariaID)
	f, err := b.form(ariaID)
	if err != nil {
		return err
	}
	f.OnCommit(fn)
	return nil
}

func (b *XwalBackend) FormVersion(ariaID string) (uint64, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return 0, err
	}
	return f.Read().Version, nil
}

// FormPatchesBetween is a read-only view on the form's published patch array;
// the caller must not retain or mutate it.
func (b *XwalBackend) FormPatchesBetween(ariaID string, after, upTo uint64) ([]VersionedPatch, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return nil, err
	}
	return f.PatchesBetween(after, upTo), nil
}

// ApplyForm returns the patch's durable index in the form channel: the append
// position, not an IR LT, since several patches can arrive between two turns.
func (b *XwalBackend) ApplyForm(ariaID string, patch message.Patch) (uint64, error) {
	b.touch(ariaID)
	version, _, err := b.ApplyFormEffectIntent(ariaID, patch, 0, Ensure)
	return version, err
}

func (b *XwalBackend) FormReclaimable(ariaID string) (bool, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return false, err
	}
	return f.Reclaimable(), nil
}

func (b *XwalBackend) ApplyFormPrivileged(ariaID string, patch message.Patch) (uint64, error) {
	v, _, err := b.ApplyFormEffectPrivilegedIf(ariaID, patch, 0)
	return v, err
}

// ApplyFormEffectPrivilegedIf: without the guard, two writers of
// system.studies silently overwrite each other.
func (b *XwalBackend) ApplyFormEffectPrivilegedIf(ariaID string, patch message.Patch,
	ifVersion uint64) (uint64, message.Patch, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return 0, message.Patch{}, err
	}
	return f.ApplyEffectPrivileged(patch, ifVersion)
}

func (b *XwalBackend) ApplyFormEffectIntent(ariaID string, patch message.Patch, ifVersion uint64, intent Intent) (uint64, message.Patch, error) {
	f, err := b.form(ariaID)
	if err != nil {
		return 0, message.Patch{}, err
	}
	return f.ApplyEffectIntent(patch, ifVersion, intent)
}

// ForkWith is the one birth verb: `fig new` spawns under the null root, `fig fork`
// forks an aria, and the patch is the child's identity.
func (b *XwalBackend) ForkWith(parent string, atMainLT uint64, patch message.Patch, opts ...BirthOption) (string, uint64, error) {
	// Every path that gives a bound form a copy is a refcount participant; this is
	// the one a live `fig fork` takes.
	if pm, _ := b.Meta(parent); pm != nil && pm.RetiredAtMS > 0 {
		patch = form.Merge(patch, form.Build(form.Snapshot{}, nil, []string{SystemRetiredKey}))
	}
	if pm, _ := b.Meta(parent); pm != nil && pm.ReapedAtMS > 0 {
		patch = form.Merge(patch, form.Build(form.Snapshot{}, nil, []string{SystemReapedKey}))
	}
	child, version, err := b.store.ForkWith(parent, atMainLT, patch, opts...)
	if err != nil {
		return "", 0, err
	}
	b.mirrorMintedBy(child, births(opts))
	b.inheritStudies(child)
	// The Form for a node born a moment ago must not be a replay of a channel
	// that was empty when someone opened it first. Close what is dropped: a
	// Form holds a writer.
	b.dropForm(child)
	b.lifetimeAtBirth(child)
	b.recordForkTurn(child)
	return child, version, nil
}

// recordForkTurn records the turn a new fork's base falls in, read from the
// parent now while its log is warm. Best effort: a fork without one is
// answered the old way by whoever next needs it, and that reader records what
// it computed.
func (b *XwalBackend) recordForkTurn(child string) {
	me, ok := b.store.trunks.InfoLight(child)
	if !ok || me.Kind != string(kindConversation) || me.Parent == "" || me.BranchedLT == 0 {
		return
	}
	parent, ok := b.store.trunks.InfoLight(me.Parent)
	if !ok || parent.Kind != string(kindConversation) {
		return
	}
	turn, exact := TurnAtLT(b.OpenNode(me.Parent), me.BranchedLT)
	if err := b.RecordBaseTurn(child, me.BranchedLT, turn, exact); err != nil {
		slog.Debug("fork: base turn not recorded", "child", child, "err", err)
	}
}

// RecordBaseTurn persists the turn a trunk's fork base falls in, in its
// parent's turn space.
func (b *XwalBackend) RecordBaseTurn(trunk string, baseLT, turn uint64, exact bool) error {
	return b.store.trunks.SetBranchedTurn(trunk, baseLT, turn, exact)
}

// CreateForm: `fig form new` spawns under the null root, `fig form fork` duplicates
// a form. Same form-cache hygiene as ForkWith.
func (b *XwalBackend) CreateForm(parent string, patch message.Patch, opts ...BirthOption) (string, uint64, error) {
	id, version, err := b.store.CreateForm(parent, patch, opts...)
	if err != nil {
		return "", 0, err
	}
	bt := births(opts)
	b.mirrorMintedBy(id, bt)
	b.dropForm(id)
	if !bt.template {
		b.lifetimeAtBirth(id)
	}
	return id, version, nil
}

func (b *XwalBackend) CreateOutfit(name string, patch message.Patch) (string, error) {
	return b.store.CreateOutfit(name, patch)
}

func (b *XwalBackend) CreateConversation(outfitID string, opts ...BirthOption) (string, error) {
	id, err := b.store.CreateConversation(outfitID)
	if err != nil {
		return "", err
	}
	b.mirrorMintedBy(id, births(opts))
	b.lifetimeAtBirth(id)
	return id, nil
}
func (b *XwalBackend) Fork(ariaID string) (cont, alt string, err error) {
	cont, alt, err = b.store.Fork(ariaID)
	if err == nil {
		b.inheritStudies(alt)
	}
	return cont, alt, err
}
func (b *XwalBackend) ForkAt(ariaID string, atMainLT uint64) (cont, alt string, err error) {
	cont, alt, err = b.store.ForkAt(ariaID, atMainLT)
	if err == nil {
		b.inheritStudies(alt)
	}
	return cont, alt, err
}

// inheritStudies is fork's half of the backref set: the id whose bound form
// declares the study is what the libretto records, so it runs on the child
// after the child exists.
func (b *XwalBackend) RetainDeclaredStudies(ariaID string) { b.inheritStudies(ariaID) }

func (b *XwalBackend) inheritStudies(ariaID string) {
	snap, err := b.FormState(ariaID)
	if err != nil {
		return // no bound form to inherit; nothing to count
	}
	for _, formID := range studiesOf(snap) {
		lib, err := b.Libretto(formID)
		if err != nil {
			slog.Warn("fork: libretto unreachable", "aria", ariaID, "form", formID, "err", err)
			continue
		}
		if _, err := lib.Retain(ariaID); err != nil {
			slog.Warn("fork: retain failed", "aria", ariaID, "form", formID, "err", err)
		}
	}
}

// Promote is cosmetic: no content moves, cached rows stay valid.
func (b *XwalBackend) Promote(ariaID string, levels int) (int, error) {
	return b.store.Promote(ariaID, levels)
}

func (b *XwalBackend) Demote(ariaID, prefer string) (string, error) {
	return b.store.Demote(ariaID, prefer)
}

func (b *XwalBackend) OwnerResolution(ariaID string, atMainLT uint64) (OwnerInfo, error) {
	o, err := b.store.OwnerOf(ariaID, atMainLT)
	if err != nil {
		return OwnerInfo{}, err
	}
	return OwnerInfo{Trunk: o.Trunk, Outfit: o.Stump, IsRoot: o.IsRoot}, nil
}

// A node's outfit is resolved here: the store knows ids and topology, the
// backend knows how to read a node.
func (b *XwalBackend) Node(id string) (NodeView, bool) {
	n, ok := b.store.Node(id)
	if ok {
		b.label(&n)
	}
	return n, ok
}

func (b *XwalBackend) Nodes() []NodeView { return b.labelAll(b.store.Nodes()) }
func (b *XwalBackend) NodeID() string    { return b.store.NodeID() }
func (b *XwalBackend) MatchID(prefix string, limit int) []string {
	return b.store.MatchID(prefix, limit)
}
func (b *XwalBackend) Conversations() []NodeView { return b.labelAll(b.store.Conversations()) }
func (b *XwalBackend) Forms() []NodeView         { return b.store.Forms() }
func (b *XwalBackend) ConversationIDs() []string { return b.store.ConversationIDs() }

type outfitLabel struct{ name, version string }

// labelAll resolves each distinct stump once; per-node locking cost more than
// the read it protected.
func (b *XwalBackend) labelAll(nodes []NodeView) []NodeView {
	// Two shapes: an aria under a stump takes the stump's label, an aria born
	// of ForkWith states its own on its form.
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

func (b *XwalBackend) labelOf(n *NodeView) outfitLabel {
	if n.Stump != "" {
		return b.stumpLabel(n.Stump)
	}
	b.mu.Lock()
	l, ok := b.labels[n.ID]
	b.mu.Unlock()
	if ok {
		return l
	}
	snap, err := b.FormState(n.ID)
	if err != nil {
		return outfitLabel{}
	}
	l = outfitLabel{name: snapString(snap, keyOutfitName), version: snapString(snap, keyOutfitVer)}
	if l.name == "" {
		l.name, l.version = snapString(snap, keyLegacyName), snapString(snap, keyLegacyVer)
	}
	b.mu.Lock()
	if b.labels == nil {
		b.labels = map[string]outfitLabel{}
	}
	b.labels[n.ID] = l
	b.mu.Unlock()
	return l
}

// labelChanged is the whole invalidation: every write to a form passes
// through this backend.
func (b *XwalBackend) labelChanged(ariaID string) {
	b.mu.Lock()
	delete(b.labels, ariaID)
	b.mu.Unlock()
}

// stumpLabel reads the birth patch, the only authoritative statement of name
// and version (a form key of the same name is the agent's mutable copy).
// Memoized for the process: id -> label is a pure function.
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
		// Only a successful read is memoized: the licence is that content cannot
		// change, which says nothing about an error.
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

func (b *XwalBackend) touch(id string) {
	b.mu.Lock()
	b.touchLocked(id)
	b.mu.Unlock()
}

// dropForm forgets and closes: a delete that only forgets leaves the writer behind.
func (b *XwalBackend) dropForm(id string) {
	b.mu.Lock()
	f := b.forms[id]
	delete(b.forms, id)
	b.mu.Unlock()
	if f != nil {
		f.Close()
	}
}

// Touch is use, not sight: a listing reads a form per row, and refreshing the
// idle clock there kept every aria resident as long as anyone ran `fig ls`.
// Opening a form only records the aria; reading its history, writing its form
// or subscribing refreshes the clock.
func (b *XwalBackend) seenLocked(id string) {
	if b.touched != nil {
		if _, ok := b.touched[id]; !ok {
			b.touched[id] = time.Now()
		}
	}
}

func (b *XwalBackend) touchLocked(id string) {
	if b.touched != nil {
		b.touched[id] = time.Now()
	}
}

// Eviction walks held, not the touch map alone: an aria whose touch entry
// was dropped would otherwise keep its caches forever.
func (b *XwalBackend) held() map[string]struct{} {
	ids := make(map[string]struct{}, len(b.touched))
	for id := range b.touched {
		ids[id] = struct{}{}
	}
	for id := range b.open {
		ids[id] = struct{}{}
	}
	for id := range b.forms {
		ids[id] = struct{}{}
	}
	for id := range b.metas {
		ids[id] = struct{}{}
	}
	return ids
}

func (b *XwalBackend) SetIRWindow(n int) {
	b.mu.Lock()
	b.irWindow = n
	b.mu.Unlock()
}

// SetTranslationBudget bounds the decoded translations of every aria
// together: one tree, one budget.
func (b *XwalBackend) SetTranslationBudget(n int) {
	b.mu.Lock()
	b.transBudget = n
	b.mu.Unlock()
	b.transTree.SetLimit(int64(n))
}

// SetIRBudget: the same, for the IR tree.
func (b *XwalBackend) SetIRBudget(n int) {
	b.mu.Lock()
	b.irBudget = n
	b.mu.Unlock()
	b.irTree.SetLimit(int64(n))
}

// TrimResident: the caller decides when, this decides how.
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

// ResidentRows is the number a window bounds, as opposed to Resident's aria count.
func (b *XwalBackend) ResidentRows() int {
	n, _ := b.residency()
	return n
}

// ResidentFormPatches is the number form_patch_window bounds.
func (b *XwalBackend) ResidentFormPatches() int {
	b.mu.Lock()
	forms := make([]*Form, 0, len(b.forms))
	for _, f := range b.forms {
		forms = append(forms, f)
	}
	b.mu.Unlock()
	n := 0
	for _, f := range forms {
		n += len(f.state.Load().patches)
	}
	return n
}

// ResidentIRBytes is the number to watch: rows are a poor proxy, the large
// entries cluster at the tail.
func (b *XwalBackend) ResidentIRBytes() int {
	_, n := b.residency()
	return n
}

func (b *XwalBackend) residency() (rows, bytes int) {
	rows, bytes, _, _ = b.residencyAll()
	return rows, bytes
}

func (b *XwalBackend) residencyAll() (irRows, irBytes, trRows, trBytes int) {
	b.mu.Lock()
	handles := make([]*ariaHandle, 0, len(b.open))
	for _, h := range b.open {
		handles = append(handles, h)
	}
	b.mu.Unlock()

	for _, h := range handles {
		if h.ir != nil {
			irRows += h.ir.Resident()
			irBytes += h.ir.ResidentBytes()
		}
		for _, t := range h.trans {
			if t == nil {
				continue
			}
			trRows += t.Resident()
			trBytes += t.ResidentBytes()
		}
	}
	return irRows, irBytes, trRows, trBytes
}

func (b *XwalBackend) ResidentTranslationRows() int {
	_, _, rows, _ := b.residencyAll()
	return rows
}

func (b *XwalBackend) ResidentTranslationBytes() int {
	_, _, _, bytes := b.residencyAll()
	return bytes
}

func (b *XwalBackend) EvictIdle(live map[string]bool, idle time.Duration) int {
	cutoff := time.Now().Add(-idle)
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for id := range b.held() {
		if live[id] {
			continue
		}
		if at, ok := b.touched[id]; ok && at.After(cutoff) {
			continue
		}
		// A form somebody is streaming from is not idle: evicting it orphans the
		// subscriber, since the next write builds a new Form the old instance
		// never hears from.
		if f := b.forms[id]; f != nil && f.Subscribed() {
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

func (b *XwalBackend) Resident() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.open)
}

// dropHandle: no xwal to close, handles own none.
func (b *XwalBackend) dropHandle(id string) {
	b.mu.Lock()
	delete(b.open, id)
	b.mu.Unlock()
}

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
	st, err := c.loadOnce(b.metaPath(ariaID))
	if err != nil {
		return nil, err
	}
	if st.Value == nil {
		return nil, nil
	}
	value := *st.Value
	if value.MetaVersion < CurrentMetaVersion {
		if up := b.healIdentity(ariaID, st, &value); up != nil {
			return up, nil
		}
	}
	return &value, nil
}

// UpdateMeta: the record has two writers (the agent's counts, a bound form commit
// mirroring system.ttl), and a whole-record SetMeta from either dropped what
// the other wrote.
func (b *XwalBackend) UpdateMeta(ariaID string, mutate func(*AriaMeta)) error {
	c := b.metaCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := c.loadOnce(b.metaPath(ariaID))
	if err != nil {
		return err
	}
	meta := &AriaMeta{}
	if st.Value != nil {
		value := *st.Value
		meta = &value
	}
	mutate(meta)
	return b.writeMetaLocked(ariaID, c, meta)
}

func (b *XwalBackend) mirrorMintedBy(id string, bt birthSpec) {
	if bt.mintedBy == "" {
		return
	}
	c := b.metaCache(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	meta := &AriaMeta{}
	st, err := c.loadOnce(b.metaPath(id))
	if err == nil && st.Value != nil {
		value := *st.Value
		meta = &value
	}
	meta.MintedBy = bt.mintedBy
	if err := b.writeMetaRawLocked(id, c, meta); err != nil {
		slog.Error("birth: minted_by not mirrored into meta", "id", id, "err", err)
	}
}

func (b *XwalBackend) writeMetaLocked(ariaID string, c *metaCache, meta *AriaMeta) error {
	if meta != nil {
		meta.MintedBy = ""
		if prev, err := c.loadOnce(b.metaPath(ariaID)); err == nil && prev.Value != nil {
			meta.MintedBy = prev.Value.MintedBy
		}
	}
	return b.writeMetaRawLocked(ariaID, c, meta)
}

func (b *XwalBackend) writeMetaRawLocked(ariaID string, c *metaCache, meta *AriaMeta) error {
	if meta != nil {
		meta.MetaVersion = CurrentMetaVersion
	}
	if err := writeJSON(b.metaPath(ariaID), meta); err != nil {
		return err
	}
	st := &metaState{}
	if meta != nil {
		value := *meta
		st.Value = &value
	}
	c.state.Store(st)
	return nil
}

func (b *XwalBackend) SetMeta(ariaID string, meta *AriaMeta) error {
	c := b.metaCache(ariaID)
	c.mu.Lock()
	err := b.writeMetaLocked(ariaID, c, meta)
	c.mu.Unlock()
	if err == nil {
		b.ttlNoted(ariaID, meta)
	}
	return err
}

// metaCache does not touch: a listing reads every sidecar, and touching there
// kept everything resident as long as anyone ran `fig ls`.
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

// Remove buries, then unlinks. The burial runs inside RemoveLeaf after its
// refusal and before its repair, so a refused delete leaves nothing sealed
// and an interrupted one leaves forms that say they are dead.
func (b *XwalBackend) DeleteSet(ariaID string) []string { return b.store.DeleteSet(ariaID) }

func (b *XwalBackend) Remove(ariaID string, recursive bool) error {
	return b.store.RemoveLeaf(ariaID, recursive, b.bury)
}

// bury tombstones each doomed form, then forgets the aria's caches, for every
// id in the set: a recursive delete must not leave children's forms resident,
// pointed at files that no longer exist.
func (b *XwalBackend) bury(doomed []string) { b.buryAs("deleted", doomed) }

func (b *XwalBackend) buryAs(reason string, doomed []string) {
	for _, id := range doomed {
		// Kill's half of the refcount, before the tombstone: a sealed form
		// cannot be read back for its study set.
		b.releaseStudies(id)
		if f, err := b.form(id); err != nil {
			slog.Warn("tombstone: form unreadable", "aria", id, "err", err)
		} else if _, err := f.Tombstone(reason); err != nil {
			slog.Warn("tombstone: not recorded", "aria", id, "err", err)
		} else if !f.Reclaimable() {
			// Deferred reclamation is not built: the unlink goes ahead and the
			// reader learns from the tombstone.
			slog.Info("tombstone: unlinking a form still being read", "aria", id)
		}
		b.dropHandle(id)
		b.dropForm(id)
		b.mu.Lock()
		delete(b.metas, id)
		delete(b.lastTS, id)
		delete(b.labels, id)
		b.mu.Unlock()
		b.ttlMu.Lock()
		delete(b.access, id)
		delete(b.ttl, id)
		b.ttlMu.Unlock()
		_ = os.Remove(b.metaPath(id))
	}
	b.ReapedForget(doomed...)
}

// releaseStudies is best-effort: a delete is the recovery path for a broken
// aria and must not be refused because a count could not be moved. The sweep
// recomputes anyway.
func (b *XwalBackend) releaseStudies(ariaID string) {
	snap, err := b.FormState(ariaID)
	if err != nil {
		return
	}
	for _, formID := range studiesOf(snap) {
		lib, err := b.Libretto(formID)
		if err != nil {
			slog.Warn("kill: libretto unreachable", "aria", ariaID, "form", formID, "err", err)
			continue
		}
		if _, err := lib.Release(ariaID); err != nil {
			slog.Warn("kill: release failed", "aria", ariaID, "form", formID, "err", err)
		}
	}
}

func (b *XwalBackend) CollectStump(stumpID string) error {
	return b.store.CollectStump(stumpID)
}

func (b *XwalBackend) Close() error {
	// Before the lock: each fold goroutine is stopped and joined, and it
	// writes to a form while it drains.
	b.closeLibrettos()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = map[string]*ariaHandle{}
	for _, f := range b.forms {
		f.Close()
	}
	b.forms = map[string]*Form{}
	b.modelsMu.Lock()
	if b.models != nil {
		b.models.Close()
		b.models = nil
	}
	b.modelsMu.Unlock()
	b.metas = map[string]*metaCache{}
	return b.store.trunks.Close() // Trunks.Close flushes the topology index
}

func (b *XwalBackend) LoadedHeads() int { return b.store.LoadedHeads() }

// Segment cache figures are process-wide: one segment file has one copy
// however many lineages read through it.
func (b *XwalBackend) SegmentCacheBytes() int64  { return SegmentCacheBytes() }
func (b *XwalBackend) SegmentCacheBudget() int64 { return SegmentCacheBudget() }
func (b *XwalBackend) SegmentCacheLoads() int64  { return SegmentCacheLoads() }

// SweepSegmentCache drops payload blocks nobody has read for `keep` sweeps.
func (b *XwalBackend) SweepSegmentCache(keep int64) (int, int64) {
	return SweepSegmentCache(keep)
}

// SweepCacheBudgets rides the daemon's standing sweep rather than a clock of
// its own: a read must never block on eviction.
func (b *XwalBackend) SweepCacheBudgets() (dropped int, freed int64) {
	d1, f1 := b.irTree.Sweep()
	d2, f2 := b.transTree.Sweep()
	d3, f3 := SweepSegmentBudget()
	return d1 + d2 + d3, f1 + f2 + f3
}

func (b *XwalBackend) LastTS(id string) int64 {
	b.mu.Lock()
	ts, ok := b.lastTS[id]
	b.mu.Unlock()
	if ok {
		return ts
	}
	ts = b.store.LastTS(id)
	if ts == 0 {
		// Not memoized: zero means "no timestamped record yet", and a node
		// that gains its first record must not read as stale forever.
		return 0
	}
	b.mu.Lock()
	if b.lastTS == nil {
		b.lastTS = map[string]int64{}
	}
	b.lastTS[id] = ts
	b.mu.Unlock()
	return ts
}

func (b *XwalBackend) wroteTo(ariaID string, at int64) {
	b.mu.Lock()
	if b.lastTS == nil {
		b.lastTS = map[string]int64{}
	}
	if at > b.lastTS[ariaID] {
		b.lastTS[ariaID] = at
	}
	b.mu.Unlock()
}

func (b *XwalBackend) SetObservedForms(ariaID string, formIDs []string) {
	b.store.SetObservedForms(ariaID, formIDs)
}

// transCacheFor: one cache per provider with a node per aria, so a fork reads
// its ancestor's translations as it reads its ancestor's IR; two providers
// hold different records for the same aria. Caller holds b.mu.
func (b *XwalBackend) transCacheFor(providerName string) *fwtree.Cache[Entry[[]json.RawMessage]] {
	if c := b.transCaches[providerName]; c != nil {
		return c
	}
	ch := transChannel(providerName)
	c := NewIRCache[[]json.RawMessage](b.transTree, func(node string) Log[[]json.RawMessage] {
		return newXwalLog[[]json.RawMessage](b.store, node, ch, false)
	}, transEntrySize, transKey[[]json.RawMessage])
	b.transCaches[providerName] = c
	return c
}
