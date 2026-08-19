package xwal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/store/log"
)

// Trunks is the trunk-addressed view of a joint xwal. A trunk is one
// continuation chain across all channels (the triune forked as a unit);
// it is the stable handle a caller addresses, while the per-fork node is
// plumbing.
type Trunks struct {
	root string
	cfg  Config
	main string

	registryRoot string

	mu sync.RWMutex
	// idx is the node/trunk index, kept in a persistent tree (index.go).
	// Trunks used to inline these maps and re-derive them by walking markers.
	idx *Index

	// version is the local rebuild cookie; rootEpoch tracks mutations made
	// through any in-process peer for the same canonical root.
	rootEpoch atomic.Uint64

	hotMu   sync.Mutex
	hot     *trunkStore
	retired map[*trunkStore]struct{}

	// ltsReg retains each node's lastTS counter across open/close/evict
	// cycles (16 bytes per node ever addressed). See docs/store/architecture.md,
	// "Recency: LastTS".
	ltsReg *lastTSRegistry

	validationMu             sync.Mutex
	validationGeneration     uint64
	validatedTopologyVersion uint64
	validatedForkBranches    map[string]uint64

	testAfterReadLock func()
	testDeepRepair    func()
}

type trunkStore struct {
	store   *log.Store
	heads   map[string]*hotHead
	refs    int
	retired bool
}

type hotHead struct {
	ready chan struct{}
	x     *XWAL
	err   error
	refs  int
}

var trunkRegistry = struct {
	sync.Mutex
	roots map[string]map[*Trunks]struct{}
}{roots: map[string]map[*Trunks]struct{}{}}

type rootTopologyState struct {
	mu        sync.Mutex
	ready     *sync.Cond
	mutating  bool
	borrowers int
	epoch     uint64
	owners    map[*Trunks]int
	lineages  map[string]*rootLineageState
}

type rootLineageState struct {
	mu      sync.Mutex
	ready   *sync.Cond
	writing bool
	owner   *Trunks
	heads   map[*Trunks]int
}

var rootTopologyRegistry = struct {
	sync.Mutex
	states map[string]*rootTopologyState
}{states: map[string]*rootTopologyState{}}

// topologyWaitTimeout bounds how long topology mutations wait for
// pending flushes and open heads before giving up.
var topologyWaitTimeout = 3 * time.Second

// ErrUnknownTrunk reports an operation addressed to a trunk id with no
// live head (never existed, removed, or relabeled away by Promote).
var ErrUnknownTrunk = errors.New("xwal: unknown trunk")

// ErrUnknownStump reports a stump name that is not a depth-1 stump node.
var ErrUnknownStump = errors.New("xwal: unknown stump")

// ErrStumpHasChildren reports a stump still hosting trunks. Removing it would
// strand them: their history begins in its birth record.
var ErrStumpHasChildren = errors.New("xwal: stump still has children")

// NodeID and TrunkID are string ids (a node id is a branch dir name; a
// trunk id is "t<N>").
type (
	NodeID  = string
	TrunkID = string
)

// genesisMarker is the root node's first main entry, so the trunk is
// immediately forkable and its prefix is never empty.
const genesisMarker = `{"genesis":true}`

// mainTail returns the main channel's last index for an opened branch.
func mainTail(x *XWAL) uint64 { return x.chans[x.main].log.LastIndex() }

// CreateTrunks initializes a fresh trunk store at dir (creating the xwal
// from cfg) and seeds the genesis at the root. The root is the channel
// directory itself: it carries NO .trunk marker (it is addressed by being
// the root, not by an id) and holds the genesis every trunk inherits.
// Stumps (CreateStump) and trunks live below it.
func createTrunks(dir string, cfg Config) (*Trunks, error) {
	if cfg.Main == "" {
		return nil, fmt.Errorf("xwal: create needs cfg.Main")
	}
	endMutation, root, err := beginRootTopologyMutation(dir)
	if err != nil {
		return nil, err
	}
	defer endMutation()
	retireTrunkStores(dir)
	x, err := Open(dir, cfg)
	if err != nil {
		return nil, err
	}
	if mainTail(x) > 0 {
		x.Close()
		return nil, fmt.Errorf("xwal: trunks already initialized at %s", dir)
	}
	gen := cfg.Genesis
	if len(gen) == 0 {
		gen = []byte(genesisMarker)
	}
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		x.Close()
		return nil, err
	}
	for _, c := range x.Channels() {
		if c.Kind == ChannelReducible {
			if _, err := x.Append(c.Name, glt, []byte("{}"), nil); err != nil {
				x.Close()
				return nil, err
			}
		}
	}
	x.Close()

	t := &Trunks{root: dir, registryRoot: root, cfg: cfg, main: cfg.Main, idx: newIndex(cfg.MintTrunkID), ltsReg: newLastTSRegistry()}
	t.cfg.ltsReg = t.ltsReg
	t.cfg.ParentOf = t.idx.ParentOf
	if err := t.rebuild(); err != nil {
		return nil, err
	}
	t.markTopologyValidated()
	if err := t.register(); err != nil {
		return nil, err
	}
	return t, nil
}

// OpenTrunks opens an existing trunk store, rebuilding its cache from disk.
func openTrunks(dir string, cfg Config) (*Trunks, error) {
	endMutation, root, err := beginRootTopologyMutation(dir)
	if err != nil {
		return nil, err
	}
	defer endMutation()
	retireTrunkStores(dir)
	man, err := loadOrCreateManifest(dir, cfg)
	if err != nil {
		return nil, err
	}
	// Before anything walks. A store in the nested v3 layout opens
	// "successfully" from here and reports its stumps and none of its
	// conversations, because RebuildFrom reads ONE directory and a v3 node
	// has no .node. There is no third path: either the store is the layout
	// this build writes, or it is refused, and Flatten is the way through.
	switch {
	case man.Layout > layoutVersion:
		return nil, fmt.Errorf("%w: store %s is layout %d, this build reads %d",
			ErrFutureLayout, dir, man.Layout, layoutVersion)
	case man.Layout < layoutVersion:
		return nil, fmt.Errorf("%w: store %s is layout %d, this build writes %d",
			ErrLegacyLayout, dir, man.Layout, layoutVersion)
	}
	man, err = recoverChannelPending(dir, cfg, man)
	if err != nil {
		return nil, err
	}
	if man, err = reconcileChannelProps(dir, cfg, man); err != nil {
		return nil, err
	}
	if man, err = materializeManifestChannels(dir, cfg, man); err != nil {
		return nil, err
	}
	x, err := Open(dir, cfg)
	if err != nil {
		return nil, err
	}
	if err := x.Close(); err != nil {
		return nil, err
	}
	main, err := mainChannelName(dir)
	if err != nil {
		return nil, err
	}
	t := &Trunks{root: dir, registryRoot: root, cfg: cfg, main: main, idx: newIndex(cfg.MintTrunkID), ltsReg: newLastTSRegistry()}
	t.cfg.ltsReg = t.ltsReg
	t.cfg.ParentOf = t.idx.ParentOf
	if err := t.rebuild(); err != nil {
		return nil, err
	}
	t.markTopologyValidated()
	if err := t.register(); err != nil {
		return nil, err
	}
	return t, nil
}

// node and head read the index; node returns nil when absent, matching the
// map lookup this replaced.
func (t *Trunks) node(key string) *NodeInfo { n, _ := t.idx.Node(key); return n }
func (t *Trunks) head(trunk string) string  { k, _ := t.idx.Head(trunk); return k }

func (t *Trunks) rebuild() error {
	oldVersion := t.Version()
	t.retireRootHotPreservingValidation()
	if err := t.idx.RebuildFrom(filepath.Join(t.root, t.main)); err != nil {
		return err
	}
	newVersion := t.Version()
	t.validationMu.Lock()
	if t.validatedTopologyVersion != 0 && t.validatedTopologyVersion == oldVersion {
		t.validatedTopologyVersion = newVersion
	}
	t.validationMu.Unlock()
	return nil
}

// Version returns the current topology version. It increases (never
// resets) every time the in-memory index is rebuilt from disk.
func (t *Trunks) Version() uint64 { return t.idx.Version() }

// Refresh re-scans the on-disk markers and re-derives the index. The escape
// hatch for a store another process has mutated.
func (t *Trunks) Refresh() error {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return err
	}
	defer endMutation()
	t.invalidateTopologyValidation()
	return t.rebuild()
}

// --- accessors ---

func (t *Trunks) hasHead(trunk string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.idx.Head(trunk)
	return ok
}

func (t *Trunks) headKey(trunk string) (string, error) {
	key, ok := t.idx.Head(trunk)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}
	return key, nil
}

func (t *Trunks) rootLineage(trunk string) *rootLineageState {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	defer state.mu.Unlock()
	lineage := state.lineages[trunk]
	if lineage == nil {
		lineage = &rootLineageState{heads: map[*Trunks]int{}}
		lineage.ready = sync.NewCond(&lineage.mu)
		state.lineages[trunk] = lineage
	}
	return lineage
}

func (t *Trunks) adoptLineage(lineage *rootLineageState) {
	if lineage.owner == nil {
		lineage.owner = t
	} else if lineage.owner != t {
		// The previous owner's buffered appends must reach disk before this
		// peer opens the lineage from disk.
		_ = lineage.owner.syncHot()
		t.retireHot()
		lineage.owner = t
	}
}

func foreignLineageHeads(lineage *rootLineageState, owner *Trunks) bool {
	for peer, count := range lineage.heads {
		if peer != owner && count != 0 {
			return true
		}
	}
	return false
}

func (t *Trunks) lockLineage(trunk string) func() {
	lineage := t.rootLineage(trunk)
	lineage.mu.Lock()
	for lineage.writing || foreignLineageHeads(lineage, t) {
		lineage.ready.Wait()
	}
	lineage.writing = true
	t.adoptLineage(lineage)
	lineage.mu.Unlock()
	return func() {
		lineage.mu.Lock()
		lineage.writing = false
		lineage.ready.Broadcast()
		lineage.mu.Unlock()
	}
}

func (t *Trunks) holdLineageHead(trunk string) func() {
	lineage := t.rootLineage(trunk)
	lineage.mu.Lock()
	for lineage.writing || foreignLineageHeads(lineage, t) {
		lineage.ready.Wait()
	}
	t.adoptLineage(lineage)
	lineage.heads[t]++
	lineage.mu.Unlock()
	return func() {
		lineage.mu.Lock()
		lineage.heads[t]--
		if lineage.heads[t] == 0 {
			delete(lineage.heads, t)
		}
		lineage.ready.Broadcast()
		lineage.mu.Unlock()
	}
}

func (t *Trunks) openHeadRefs() int {
	t.hotMu.Lock()
	defer t.hotMu.Unlock()
	refs := 0
	if t.hot != nil {
		refs += t.hot.refs
	}
	for h := range t.retired {
		refs += h.refs
	}
	return refs
}

func (t *Trunks) ensureNoOpenHeads() error {
	deadline := time.Now().Add(topologyWaitTimeout)
	for {
		refs := t.openHeadRefs()
		if refs == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("xwal: topology mutation timed out waiting for %d open head(s)", refs)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (t *Trunks) borrowHotUntracked(branch []string) (*XWAL, func() error, error) {
	t.hotMu.Lock()
	h := t.hot
	if h == nil {
		h = &trunkStore{store: log.NewStore(), heads: map[string]*hotHead{}}
		t.hot = h
	}
	h.refs++
	key := strings.Join(branch, "\x00")
	head := h.heads[key]
	creator := head == nil
	if creator {
		head = &hotHead{ready: make(chan struct{})}
		h.heads[key] = head
	}
	head.refs++
	t.hotMu.Unlock()

	if creator {
		head.x, head.err = open(t.root, t.cfg, h.store, branch...)
		t.hotMu.Lock()
		if head.err != nil && h.heads[key] == head {
			delete(h.heads, key)
		}
		close(head.ready)
		t.hotMu.Unlock()
	} else {
		<-head.ready
	}
	if head.err != nil {
		t.hotMu.Lock()
		head.refs--
		t.hotMu.Unlock()
		_ = t.releaseHot(h)
		return nil, nil, head.err
	}

	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			t.hotMu.Lock()
			head.refs--
			t.hotMu.Unlock()
			releaseErr = t.releaseHot(h)
		})
		return releaseErr
	}
	return head.x, release, nil
}

// evictLineage unloads a trunk's hot head: buffered entries are flushed
// under the lineage lock (appenders excluded), then the head and its
// channel snapshots are dropped; the next touch reloads from disk. A
// head still borrowed is skipped. Returns whether the lineage is now
// unloaded.
func (t *Trunks) evictLineage(trunk string) (bool, error) {
	unlock := t.lockLineage(trunk)
	defer unlock()
	t.mu.RLock()
	headKey, ok := t.idx.Head(trunk)
	var branch []string
	if ok {
		branch = []string{headKey}
	}
	t.mu.RUnlock()
	if !ok {
		return true, nil
	}
	key := strings.Join(branch, "\x00")

	t.hotMu.Lock()
	h := t.hot
	if h == nil {
		t.hotMu.Unlock()
		return true, nil
	}
	head := h.heads[key]
	if head == nil {
		t.hotMu.Unlock()
		return true, nil
	}
	if head.refs != 0 || head.x == nil || head.err != nil {
		t.hotMu.Unlock()
		return false, nil
	}
	t.hotMu.Unlock()

	for _, name := range head.x.order {
		if err := head.x.chans[name].log.Sync(); err != nil {
			return false, err
		}
	}

	t.hotMu.Lock()
	if t.hot != h || h.heads[key] != head || head.refs != 0 {
		t.hotMu.Unlock()
		return false, nil
	}
	delete(h.heads, key)
	inUse := map[string]bool{}
	for _, other := range h.heads {
		if other.x == nil {
			continue
		}
		for _, ch := range other.x.chans {
			inUse[ch.dir] = true
		}
	}
	t.hotMu.Unlock()

	for _, name := range head.x.order {
		ch := head.x.chans[name]
		if inUse[ch.dir] {
			continue
		}
		if err := h.store.Evict(ch.dir); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (t *Trunks) openHotTopology(key string) (*XWAL, error) {
	var branch []string
	if key != "" {
		branch = []string{key}
	}
	x, release, err := t.borrowHotUntracked(branch)
	if err != nil {
		return nil, err
	}
	return x.sharedView(release, nil, t.retireRootHotPreservingValidation), nil
}

func rootTopologyStateFor(root string) *rootTopologyState {
	rootTopologyRegistry.Lock()
	defer rootTopologyRegistry.Unlock()
	state := rootTopologyRegistry.states[root]
	if state == nil {
		state = &rootTopologyState{
			owners:   map[*Trunks]int{},
			lineages: map[string]*rootLineageState{},
		}
		state.ready = sync.NewCond(&state.mu)
		rootTopologyRegistry.states[root] = state
	}
	return state
}

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func beginRootTopologyMutation(root string) (func(), string, error) {
	return beginRootTopologyMutationFor(root, nil)
}

var errLocalTopologyBorrowers = errors.New("xwal: local topology borrowers")

func beginRootTopologyMutationFor(root string, owner *Trunks) (func(), string, error) {
	root, err := canonicalRoot(root)
	if err != nil {
		return nil, "", err
	}
	state := rootTopologyStateFor(root)
	deadline := time.Now().Add(topologyWaitTimeout)
	state.mu.Lock()
	for {
		for state.mutating {
			state.ready.Wait()
		}
		if state.borrowers == 0 {
			break
		}
		borrowers := state.borrowers
		local := state.owners[owner]
		if owner != nil && local == borrowers {
			state.mu.Unlock()
			return nil, "", errLocalTopologyBorrowers
		}
		if time.Now().After(deadline) {
			state.mu.Unlock()
			return nil, "", fmt.Errorf("xwal: topology mutation timed out waiting for %d open head(s)", borrowers)
		}
		state.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		state.mu.Lock()
	}
	state.mutating = true
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		state.mutating = false
		state.epoch++
		if owner != nil {
			owner.rootEpoch.Store(state.epoch)
		}
		state.ready.Broadcast()
		state.mu.Unlock()
	}, root, nil
}

func beginRootAdditiveMutation(root string) (func(), error) {
	root, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	for state.mutating {
		state.ready.Wait()
	}
	state.mutating = true
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		state.mutating = false
		state.ready.Broadcast()
		state.mu.Unlock()
	}, nil
}

func beginRootBorrow(root string, owner *Trunks) error {
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	defer state.mu.Unlock()
	for state.mutating {
		state.ready.Wait()
	}
	state.borrowers++
	state.owners[owner]++
	return nil
}

func endRootBorrow(root string, owner *Trunks) {
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	state.borrowers--
	state.owners[owner]--
	if state.owners[owner] == 0 {
		delete(state.owners, owner)
	}
	state.ready.Broadcast()
	state.mu.Unlock()
}

func transferRootBorrow(root string, from, to *Trunks) {
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	state.owners[from]--
	if state.owners[from] == 0 {
		delete(state.owners, from)
	}
	state.owners[to]++
	state.ready.Broadcast()
	state.mu.Unlock()
}

func waitRootBorrowers(root string, owner *Trunks) error {
	state := rootTopologyStateFor(root)
	deadline := time.Now().Add(topologyWaitTimeout)
	state.mu.Lock()
	for state.owners[owner] != 0 {
		if time.Now().After(deadline) {
			n := state.owners[owner]
			state.mu.Unlock()
			return fmt.Errorf("xwal: topology mutation timed out waiting for %d open head(s)", n)
		}
		state.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		state.mu.Lock()
	}
	state.mu.Unlock()
	return nil
}

func rootTopologyEpoch(root string) uint64 {
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.epoch
}

func (t *Trunks) ensureCurrentTopology() error {
	epoch := rootTopologyEpoch(t.registryRoot)
	if t.rootEpoch.Load() == epoch {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	epoch = rootTopologyEpoch(t.registryRoot)
	if t.rootEpoch.Load() == epoch {
		return nil
	}
	if err := t.rebuild(); err != nil {
		return err
	}
	t.rootEpoch.Store(epoch)
	return nil
}

func (t *Trunks) beginTrackedRead() (func(), error) {
	if err := beginRootBorrow(t.registryRoot, t); err != nil {
		return nil, err
	}
	if err := t.ensureCurrentTopology(); err != nil {
		endRootBorrow(t.registryRoot, t)
		return nil, err
	}
	t.mu.RLock()
	if t.testAfterReadLock != nil {
		t.testAfterReadLock()
	}
	return func() {
		endRootBorrow(t.registryRoot, t)
		t.mu.RUnlock()
	}, nil
}

// beginFlatCreate is the mutation gate for a creator. A flat fork writes
// only its own new sibling directory and one index entry: it never freezes,
// re-homes or rewrites anything a live head is reading, so it takes no root
// topology mutation and does NOT wait for other arias to close their heads.
// That is the whole point of the flat layout — an aria forks itself without
// blocking anyone.
func (t *Trunks) beginFlatCreate() (func(), error) {
	t.mu.Lock()
	if err := t.syncHot(); err != nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("xwal: refusing create, hot flush failing: %w", err)
	}
	// A PEER moved the tree: re-read it. Cheap (one atomic load) and
	// skipping it would let us fork from a node we do not know about.
	if epoch := rootTopologyEpoch(t.registryRoot); t.rootEpoch.Load() != epoch {
		if err := t.rebuild(); err != nil {
			t.mu.Unlock()
			return nil, err
		}
		t.rootEpoch.Store(epoch)
	}
	return func() { t.mu.Unlock() }, nil
}

func (t *Trunks) beginTopologyMutation() (func(), error) {
	for {
		t.mu.Lock()
		end, _, err := beginRootTopologyMutationFor(t.registryRoot, t)
		if errors.Is(err, errLocalTopologyBorrowers) {
			if openErr := t.ensureNoOpenHeads(); openErr != nil {
				t.mu.Unlock()
				return nil, openErr
			}
			t.mu.Unlock()
			if waitErr := waitRootBorrowers(t.registryRoot, t); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		// A failing flush must fail the topology op loudly: retiring the
		// hot store would silently drop the unflushed (acked) tail.
		if err := t.syncHot(); err != nil {
			end()
			t.mu.Unlock()
			return nil, fmt.Errorf("xwal: refusing topology mutation, hot flush failing: %w", err)
		}
		// Re-read the tree only when a PEER moved it. end() stamps the new
		// epoch onto us, so after our own mutation we are already current and
		// walking again teaches nothing. A repair that rewrites markers calls
		if epoch := rootTopologyEpoch(t.registryRoot); t.rootEpoch.Load() != epoch {
			if err := t.rebuild(); err != nil {
				end()
				t.mu.Unlock()
				return nil, err
			}
			t.rootEpoch.Store(epoch)
		}
		return func() {
			end()
			t.mu.Unlock()
		}, nil
	}
}

func (t *Trunks) releaseHot(h *trunkStore) error {
	t.hotMu.Lock()
	h.refs--
	closeStore := h.retired && h.refs == 0
	if closeStore {
		delete(t.retired, h)
	}
	t.hotMu.Unlock()
	if closeStore {
		return h.store.Close()
	}
	return nil
}

// syncHot flushes every loaded head lineage-coherently (main first,
// related channels capped at durable main referents), so the stray
// sweep can never push a related record ahead of its main entry.
func (t *Trunks) syncHot() error {
	t.hotMu.Lock()
	h := t.hot
	if h != nil {
		h.refs++
	}
	var heads []*hotHead
	if h != nil {
		for _, head := range h.heads {
			heads = append(heads, head)
			head.refs++
		}
	}
	t.hotMu.Unlock()
	if h == nil {
		return nil
	}
	var err error
	for _, head := range heads {
		select {
		case <-head.ready:
		default:
			continue
		}
		if head.x == nil || head.err != nil {
			continue
		}
		if ferr := head.x.syncCoherent(); ferr != nil && err == nil {
			err = ferr
		}
	}
	t.hotMu.Lock()
	for _, head := range heads {
		head.refs--
	}
	t.hotMu.Unlock()
	if rerr := t.releaseHot(h); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

func (t *Trunks) retireHot() {
	t.hotMu.Lock()
	h := t.hot
	t.hot = nil
	if h != nil {
		h.retired = true
		if h.refs != 0 {
			if t.retired == nil {
				t.retired = map[*trunkStore]struct{}{}
			}
			t.retired[h] = struct{}{}
		}
	}
	closeStore := h != nil && h.refs == 0
	t.hotMu.Unlock()
	if closeStore {
		_ = h.store.Close()
	}
}

func (t *Trunks) register() error {
	if t.registryRoot == "" {
		root, err := canonicalRoot(t.root)
		if err != nil {
			return err
		}
		t.registryRoot = root
	}
	trunkRegistry.Lock()
	peers := trunkRegistry.roots[t.registryRoot]
	if peers == nil {
		peers = map[*Trunks]struct{}{}
		trunkRegistry.roots[t.registryRoot] = peers
	}
	peers[t] = struct{}{}
	trunkRegistry.Unlock()
	t.rootEpoch.Store(rootTopologyEpoch(t.registryRoot))
	return nil
}

// retireRootHot drops the root topology head. Every flat fork opens it to
// read the parent's channel tails, and it belongs to no trunk, so lineage
// eviction can never reach it.
func (t *Trunks) retireRootHot() {
	invalidateRootTopologyValidation(t.registryRoot)
	retireTrunkStores(t.root)
}

func (t *Trunks) retireRootHotPreservingValidation() {
	retireTrunkStores(t.root)
}

func invalidateRootTopologyValidation(root string) {
	trunkRegistry.Lock()
	peers := make([]*Trunks, 0, len(trunkRegistry.roots[root]))
	for peer := range trunkRegistry.roots[root] {
		peers = append(peers, peer)
	}
	trunkRegistry.Unlock()
	for _, peer := range peers {
		peer.invalidateTopologyValidation()
	}
}

func (t *Trunks) invalidateTopologyValidation() {
	t.validationMu.Lock()
	t.validationGeneration++
	t.validatedTopologyVersion = 0
	t.validatedForkBranches = nil
	t.validationMu.Unlock()
}

func (t *Trunks) markTopologyValidated() {
	t.validationMu.Lock()
	if t.validationGeneration == 0 {
		t.validationGeneration = 1
	}
	t.validatedTopologyVersion = t.Version()
	t.validatedForkBranches = nil
	t.validationMu.Unlock()
}

func retireTrunkStores(root string) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return
	}
	registryRoot := filepath.Clean(abs)
	trunkRegistry.Lock()
	peers := make([]*Trunks, 0, len(trunkRegistry.roots[registryRoot]))
	for peer := range trunkRegistry.roots[registryRoot] {
		peers = append(peers, peer)
	}
	trunkRegistry.Unlock()
	if len(peers) == 0 {
		return
	}
	for _, peer := range peers {
		peer.retireHot()
	}
}

// Close releases cached segment handles. Any Head or StumpHead handles must
// be closed first. The topology cache remains usable and opens a fresh
// segment generation on the next disk-backed operation.
func (t *Trunks) Close() error {
	t.hotMu.Lock()
	refs := 0
	if t.hot != nil {
		refs += t.hot.refs
	}
	for h := range t.retired {
		refs += h.refs
	}
	if refs != 0 {
		t.hotMu.Unlock()
		return fmt.Errorf("xwal: close trunks with %d open head(s)", refs)
	}
	stores := make([]*trunkStore, 0, 1+len(t.retired))
	if t.hot != nil {
		t.hot.retired = true
		stores = append(stores, t.hot)
	}
	for h := range t.retired {
		stores = append(stores, h)
	}
	t.hot = nil
	t.retired = nil
	t.hotMu.Unlock()

	trunkRegistry.Lock()
	if peers := trunkRegistry.roots[t.registryRoot]; peers != nil {
		delete(peers, t)
		if len(peers) == 0 {
			delete(trunkRegistry.roots, t.registryRoot)
		}
	}
	trunkRegistry.Unlock()
	var errs []error
	for _, h := range stores {
		if err := h.store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// EnsureChannel adds and backfills a channel if needed, and installs its
// runtime policy for subsequent hot heads and topology operations.
func (t *Trunks) ensureChannel(spec ChannelSpec) error {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return err
	}
	defer endMutation()

	man, err := loadOrCreateManifest(t.root, t.cfg)
	if err != nil {
		return err
	}
	man, err = recoverChannelPending(t.root, t.cfg, man)
	if err != nil {
		return err
	}
	if err := validateChannelSpec(t.root, t.cfg, man, spec); err != nil {
		return err
	}
	if err := t.ensureNoOpenHeads(); err != nil {
		return err
	}

	cfg := withChannelSpec(t.cfg, spec)
	t.retireRootHotPreservingValidation()
	pending := channelPendingPlan{Channel: spec.manifest()}
	if err := writeChannelPending(t.root, pending); err != nil {
		return err
	}
	if _, err := recoverChannelPending(t.root, cfg, man); err != nil {
		return err
	}

	t.cfg = cfg
	return t.rebuild()
}

// Head opens the live head node of a trunk. Caller closes it.
func (t *Trunks) Head(trunk string) (*XWAL, error) {
	releaseLineage := t.holdLineageHead(trunk)
	if err := beginRootBorrow(t.registryRoot, t); err != nil {
		releaseLineage()
		return nil, err
	}
	if err := t.ensureCurrentTopology(); err != nil {
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	t.mu.RLock()
	branch, err := t.headKey(trunk)
	if err != nil {
		t.mu.RUnlock()
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	t.mu.RUnlock()
	if err != nil {
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	view := x.sharedView(release, func() {
		endRootBorrow(t.registryRoot, t)
	}, t.retireRootHotPreservingValidation)
	view.releaseLineage = releaseLineage
	view.borrowRoot = t.registryRoot
	view.borrowOwner = t
	return view, nil
}

// Append adds a main-timeline entry at the trunk's tail. atMainLT is
// ignored — appends never fork; ForkAt is the only forking path.
// AppendCursors is Append for the MAIN channel with extra cursor
// entries (see XWAL.AppendMainCursors), and it hands back THE STAMP IT
// WROTE so a caller need not read the record to learn it.
func (t *Trunks) AppendCursors(trunk string, payload, meta []byte, extra map[string]uint64) (string, uint64, map[string]uint64, error) {
	return t.appendMainWith(trunk, payload, meta, extra)
}

func (t *Trunks) Append(trunk string, atMainLT uint64, payload, meta []byte) (string, uint64, error) {
	_ = atMainLT
	node, lt, _, err := t.appendMainWith(trunk, payload, meta, nil)
	return node, lt, err
}

func (t *Trunks) appendMainWith(trunk string, payload, meta []byte, extra map[string]uint64) (string, uint64, map[string]uint64, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()

	endRead, err := t.beginTrackedRead()
	if err != nil {
		return "", 0, nil, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return "", 0, nil, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return "", 0, nil, err
	}
	lt, cursors, appendErr := x.AppendMainCursors(payload, meta, extra)
	_ = release()
	if appendErr != nil {
		return "", 0, nil, appendErr
	}
	return trunk, lt, cursors, nil
}

// ownerOf is the nearest ancestor of node whose own range covers atMainLT.
// It climbs the LINEAGE, not the directory: every flat node is depth-1, so
// a path walk sees only the node itself and falls through to the root.
// The root owns [1..]; it is returned as "".
func (t *Trunks) ownerOf(node string, atMainLT uint64) (string, error) {
	for hops := t.idx.Len(); node != ""; node = t.idx.ParentOf(node) {
		if hops--; hops < 0 {
			return "", fmt.Errorf("xwal: lineage cycle below %q", node)
		}
		fb, err := t.readForkBase(node)
		if err != nil {
			return "", err
		}
		if fb <= atMainLT {
			return node, nil
		}
	}
	return "", nil
}

func (t *Trunks) readForkBase(key string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(t.irDir(key), ".fork"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "base="); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, fmt.Errorf("xwal: malformed fork marker for %q", key)
}

// ForkTail bisects a trunk's present.
//   - head has content: freeze it; the continuation keeps the trunk (new
//     empty head) and a new alternative trunk is founded.
//   - head is empty: it has no LT of its own, so redirect — N-ary fork the
//     PARENT, adding one new alternative sibling trunk; the trunk keeps its
//     empty head untouched.
func (t *Trunks) ForkTail(trunk string) (string, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	return t.forkTailLocked(trunk)
}

func (t *Trunks) forkTailLocked(trunk string) (string, error) {
	headKey, ok := t.idx.Head(trunk)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}
	return t.forkFlat(headKey, "conversation", true)
}

// forkFlat mints a depth-1 sibling sharing the parent prefix. Nothing the
// parent owns is written: no freeze, no continuation, no rehome.
func (t *Trunks) forkFlat(parentKey, kind string, mintTrunk bool) (string, error) {
	child := t.mintNode()
	var trunk string
	if mintTrunk {
		trunk = t.mintTrunk(kind)
	}
	bases, err := t.channelBases(parentKey, 0)
	if err != nil {
		return "", err
	}
	for name, base := range bases {
		dir := filepath.Join(t.root, name, fsName(child))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), fmt.Appendf(nil, "base=%d\n", base)); err != nil {
			return "", err
		}
	}
	// One marker, written last: it IS the commit point, and writing lineage
	// and trunk id as a pair left a node with one and not the other when a
	// crash landed between them.
	if err := writeNodeMarker(t.irDir(child), nodeMarker{from: parentKey, kind: kind, trunk: trunk}); err != nil {
		return "", err
	}
	t.idx.SpawnFlat(parentKey, child, trunk, kind)
	if !mintTrunk {
		return child, nil
	}
	return trunk, nil
}

// forkFlatNamed is forkFlat with a caller-chosen node name (stumps).
func (t *Trunks) forkFlatNamed(parentKey, name, kind string) error {
	bases, err := t.channelBases(parentKey, 0)
	if err != nil {
		return err
	}
	for chName, base := range bases {
		dir := filepath.Join(t.root, chName, fsName(name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), fmt.Appendf(nil, "base=%d\n", base)); err != nil {
			return err
		}
	}
	if err := writeNodeMarker(t.irDir(name), nodeMarker{from: parentKey, kind: kind}); err != nil {
		return err
	}
	t.idx.SpawnFlat(parentKey, name, "", kind)
	return nil
}

// channelBases is the fork base for every channel of node, sharing main
// [1..at]. ONE rule for tail and interior forks alike: main starts at at+1,
// and a related channel inherits only what is keyed at or below at, never
// past what the parent itself exposes, never below where its segments start.
func (t *Trunks) channelBases(node string, at uint64) (map[string]uint64, error) {
	x, err := t.openHotTopology(node)
	if err != nil {
		return nil, err
	}
	// A TAIL fork means "everything as it stands now", and for an unkeyed
	// channel that includes records written since the last turn -- a board
	// patch is a declaration, and one made after the last turn is still part
	// of what the child should start from. An INTERIOR fork means "as of
	// turn N", which is exactly what N's cursor stamp says.
	tailFork := at == 0
	if tailFork {
		at = mainTail(x)
	}
	out := map[string]uint64{}
	for _, c := range x.Channels() {
		var base uint64
		switch {
		case c.Name == t.main:
			base = at + 1
		case x.chans[c.Name].unkeyed && tailFork:
			base = c.Last + 1
		case x.chans[c.Name].unkeyed:
			// An unkeyed channel carries no main LT, so "records keyed at or
			// below the fork point" is not a question that can be asked. The
			// main record AT the fork point already recorded where this
			// channel stood; that stamp IS the boundary.
			cur, cerr := x.CursorAt(at, c.Name)
			if cerr != nil {
				x.Close()
				return nil, cerr
			}
			base = cur + 1
			// Same ceiling as the keyed branch, for the same reason. A
			// cursor is DATA ON DISK: stale, repaired, or written by an
			// older build, it can name a position the parent does not have.
			// A base above the parent's own tail leaves the child numbering
			// over a hole in its own prefix, and the first read through the
			// gap kills the process.
			if base > c.Last+1 {
				base = c.Last + 1
			}
		default:
			lt, ok, lerr := x.chans[c.Name].lookupAtOrBelow(at)
			if lerr != nil {
				x.Close()
				return nil, lerr // a failed lookup is not "found nothing"
			}
			if !ok {
				lt = 0
			}
			base = lt + 1
			if base > c.Last+1 {
				base = c.Last + 1
			}
		}
		if base < c.First {
			base = c.First // channel empty here; inherit from the ancestor
		}
		out[c.Name] = base
	}
	x.Close()
	return out, nil
}

// ForkAt forks a trunk at an interior main-LT WITHOUT appending: it shares
// [1..atMainLT] and creates an EMPTY alternative trunk diverging at
// atMainLT+1; the original trunk keeps its id and its suffix. At or past the
// tail it degenerates to a tail fork (ForkTail). Returns the new alternative
// trunk. (Append does fork+send in one; ForkAt is the imperative-only fork.)
func (t *Trunks) ForkAt(trunk string, atMainLT uint64) (string, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	branch, err := t.headKey(trunk)
	if err != nil {
		return "", err
	}
	x, err := t.openHotTopology(branch)
	if err != nil {
		return "", err
	}
	tail := mainTail(x)
	ownFirst := ownFirstIdx(x)
	x.Close()

	if atMainLT == 0 || atMainLT >= tail {
		return t.forkTailLocked(trunk)
	}
	if atMainLT < ownFirst {
		// Flat: fork the ancestor that owns atMainLT, in place.
		t.retireRootHotPreservingValidation()
		owner, oerr := t.ownerOf(branch, atMainLT)
		if oerr != nil {
			return "", oerr
		}
		return t.forkFlatAt(owner, atMainLT)
	}

	// Interior fork: a sibling sharing [1..atMainLT]. The parent keeps its
	// suffix; nothing it owns is rewritten.
	return t.forkFlatAt(branch, atMainLT)
}

// forkFlatAt is forkFlat sharing a prefix that ends at an interior main LT.
// Related channels share up to their last entry keyed at or below it.
func (t *Trunks) forkFlatAt(parentKey string, atMainLT uint64) (string, error) {
	child := t.mintNode()
	// Interior forks re-split conversations; nothing else has a tail to
	// split at an interior LT.
	trunk := t.mintTrunk("conversation")
	bases, err := t.channelBases(parentKey, atMainLT)
	if err != nil {
		return "", err
	}
	for name, base := range bases {
		dir := filepath.Join(t.root, name, fsName(child))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), fmt.Appendf(nil, "base=%d\n", base)); err != nil {
			return "", err
		}
	}
	if err := writeNodeMarker(t.irDir(child), nodeMarker{from: parentKey, kind: "conversation", trunk: trunk}); err != nil {
		return "", err
	}
	t.idx.SpawnFlat(parentKey, child, trunk, "conversation")
	return trunk, nil
}

// Remove deletes a trunk: its founding node's entire subtree, in every
// channel. Trunk-addressed (a node is plumbing). Refuses the root trunk, and
// refuses a trunk that has live branches (descendant trunks branched off it)
// unless recursive — in which case those branches go too.
func (t *Trunks) Remove(trunk string, recursive bool) error {
	_, err := t.remove(trunk, recursive)
	return err
}

// remove deletes a trunk's founding subtree and reports every trunk id
// that ceased to exist, so lifecycle bookkeeping above can purge them.
func (t *Trunks) remove(trunk string, recursive bool) ([]TrunkID, error) {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return nil, err
	}
	defer endMutation()
	if err := t.ensureNoOpenHeads(); err != nil {
		return nil, err
	}
	t.retireRootHotPreservingValidation()

	// The founding node is the shallowest node carrying this trunk (its
	// parent is in another trunk, or it is the root).
	foundKey, ok := "", false
	for key, n := range t.idx.All() {
		if n.Trunk != trunk {
			continue
		}
		if n.IsRoot {
			return nil, fmt.Errorf("xwal: cannot remove the root trunk %q", trunk)
		}
		if p := t.node(n.From); p == nil || p.Trunk != trunk {
			foundKey, ok = key, true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}

	// Collect the trunks living in the founding node's subtree.
	sub := map[string]bool{}
	var keys []string
	kids := t.idx.ChildIndex()
	var walk func(string)
	walk = func(key string) {
		n := t.node(key)
		if n == nil {
			return
		}
		keys = append(keys, key)
		if n.Trunk != "" {
			sub[n.Trunk] = true
		}
		for _, c := range kids[key] {
			walk(c)
		}
	}
	walk(foundKey)
	delete(sub, trunk)
	if len(sub) > 0 && !recursive {
		return nil, fmt.Errorf("xwal: trunk %q has %d live branch(es); remove recursively to take them too", trunk, len(sub))
	}
	removed := []TrunkID{trunk}
	for id := range sub {
		removed = append(removed, id)
	}

	// EVERY node of the subtree, in every channel. The layout is flat, so a
	// descendant is a sibling directory: taking the founding node alone
	// leaves the rest behind pointing at a .from that is no longer there,
	// and the index reads those orphans back as roots.
	names, err := channelNames(t.root)
	if err != nil {
		return nil, err
	}
	for _, ch := range names {
		for _, key := range keys {
			if err := os.RemoveAll(filepath.Join(t.root, ch, fsName(key))); err != nil {
				return nil, err
			}
		}
	}
	return removed, t.rebuild()
}

// SpawnChild adds a new child trunk under a "ceremonial" parent trunk
// WITHOUT a continuation: the parent's node becomes (or stays) a frozen
// branch point that only hosts children. (ForkTail, by contrast, gives the
// parent a continuation.) Returns the new child trunk id.
func (t *Trunks) SpawnChild(parent TrunkID) (TrunkID, error) {
	return t.SpawnChildKind(parent, "conversation")
}

// SpawnChildKind is SpawnChild with a caller-chosen node kind. This is how
// a live, patchable trunk hosts children of a different species — figaro
// binds a figaro out of an unbound form with kind "conversation", and
// forks form from form with kind "form". The parent is not written, not
// frozen, and stays appendable: a fork snapshots the parent's channels at
// their bases and later parent appends belong to the parent alone.
func (t *Trunks) SpawnChildKind(parent TrunkID, kind string) (TrunkID, error) {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	nodeKey, ok := t.anchorOf(parent)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownTrunk, parent)
	}
	return t.forkFlat(nodeKey, kind, true)
}

// CreateStump mints a markerless, named depth-1 child of the root — the
// cauterization boundary. A stump holds its own birth content (write it via
// StumpHead) and hosts top-level trunks as children (SpawnUnderStump). It
// carries NO .trunk marker: its name + depth-1 position IS its identity
// (figaro names them <loadout>@<hash>). Idempotent callers should check
// Stumps() first; a duplicate name is an error.
func (t *Trunks) CreateStump(name string) error {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("xwal: invalid stump name %q", name)
	}
	if n := t.node(name); n != nil {
		return fmt.Errorf("xwal: stump %q already exists", name)
	}
	if err := t.forkFlatNamed("", name, "loadout"); err != nil {
		return fmt.Errorf("xwal: create-stump %q: %w", name, err)
	}
	return nil
}

// RemoveStump deletes a childless stump.
func (t *Trunks) RemoveStump(name string) error {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return err
	}
	defer endMutation()
	if err := t.ensureNoOpenHeads(); err != nil {
		return err
	}
	t.retireRootHotPreservingValidation()

	n := t.node(name)
	if n == nil || n.Kind != "loadout" {
		return fmt.Errorf("%w %q", ErrUnknownStump, name)
	}
	if kids := t.idx.ChildrenOf(name); len(kids) > 0 {
		return fmt.Errorf("%w: %q has %d", ErrStumpHasChildren, name, len(kids))
	}

	names, err := channelNames(t.root)
	if err != nil {
		return err
	}
	for _, ch := range names {
		if err := os.RemoveAll(filepath.Join(t.root, ch, fsName(name))); err != nil {
			return err
		}
	}
	return t.rebuild()
}

// StumpHead opens a stump's branch for appending its birth content (IR +
// related channels), before it gains trunk children. Caller closes it.
func (t *Trunks) StumpHead(name string) (*XWAL, error) {
	releaseLineage := t.holdLineageHead("stump:" + name)
	if err := beginRootBorrow(t.registryRoot, t); err != nil {
		releaseLineage()
		return nil, err
	}
	if err := t.ensureCurrentTopology(); err != nil {
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	t.mu.RLock()
	n := t.node(name)
	if n == nil || n.Kind != "loadout" {
		t.mu.RUnlock()
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, fmt.Errorf("xwal: no stump %q", name)
	}
	x, release, err := t.borrowHotUntracked([]string{name})
	t.mu.RUnlock()
	if err != nil {
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	view := x.sharedView(release, func() {
		endRootBorrow(t.registryRoot, t)
	}, t.retireRootHotPreservingValidation)
	view.releaseLineage = releaseLineage
	view.borrowRoot = t.registryRoot
	view.borrowOwner = t
	return view, nil
}

// SpawnUnderStump mints a new trunk (a top-level aria) as a child of a stump.
func (t *Trunks) SpawnUnderStump(name string) (TrunkID, error) {
	return t.SpawnUnderStumpKind(name, "conversation")
}

// SpawnUnderStumpKind is SpawnUnderStump with a caller-chosen node kind,
// recorded in the child's marker and reported by listings.
func (t *Trunks) SpawnUnderStumpKind(name, kind string) (TrunkID, error) {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	if n := t.node(name); n == nil || n.Kind != "loadout" {
		return "", fmt.Errorf("xwal: no stump %q", name)
	}
	return t.forkFlat(name, kind, true)
}

// SpawnUnderRoot mints a new trunk directly under the root (a top-level
// trunk with no stump — e.g. a loadoutless conversation).
func (t *Trunks) SpawnUnderRoot() (TrunkID, error) {
	return t.SpawnUnderRootKind("conversation")
}

// SpawnUnderRootKind is SpawnUnderRoot with a caller-chosen node kind —
// how a consumer mints something under the root that is NOT a
// conversation (figaro's unbound forms fork the null root with kind
// "form"). The kind lands in the node marker, is immutable from then on
// (a fork never converts), and is what listings discriminate by.
func (t *Trunks) SpawnUnderRootKind(kind string) (TrunkID, error) {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	return t.forkFlat("", kind, true)
}

// OwnerTrunk returns the trunk id of the node that OWNS atMainLT along the
// given trunk's lineage — the deepest ancestor whose own segments contain it.
// Callers layer policy on this: e.g. an LT owned by a "ceremonial" trunk (a
// null root or loadout) can be redirected to SpawnChild instead of a re-split.
func (t *Trunks) OwnerTrunk(trunk string, atMainLT uint64) (string, error) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return "", err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return "", err
	}
	ob, err := t.ownerOf(branch, atMainLT)
	if err != nil {
		return "", err
	}
	n := t.node(ob)
	if n == nil {
		return "", fmt.Errorf("xwal: no owner node for main-LT %d on trunk %q", atMainLT, trunk)
	}
	return n.Trunk, nil
}

// Owner describes which node owns a main-LT along a trunk's lineage: a
// trunk (Trunk set), a stump (Stump set), or the root (IsRoot). Callers
// layer policy on this — e.g. an LT owned by the root or a stump is
// ceremonial, so a "fork there" spawns a fresh child rather than re-splitting.
type Owner struct {
	Trunk  TrunkID // "" if the owner is the root or a stump
	Stump  string  // the stump name, if the owner is a stump
	IsRoot bool
}

// Owner resolves which node owns atMainLT along the given trunk's lineage.
func (t *Trunks) Owner(trunk TrunkID, atMainLT uint64) (Owner, error) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return Owner{}, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return Owner{}, err
	}
	ob, err := t.ownerOf(branch, atMainLT)
	if err != nil {
		return Owner{}, err
	}
	n := t.node(ob)
	if n == nil {
		return Owner{}, fmt.Errorf("xwal: no owner node for main-LT %d on trunk %q", atMainLT, trunk)
	}
	return Owner{Trunk: n.Trunk, Stump: stumpName(ob, n), IsRoot: n.IsRoot}, nil
}

// StumpInfo is a read-only view of a stump.
type StumpInfo struct {
	Name     string
	Children []TrunkID // trunk ids of its immediate trunk children
}

// Stumps returns every stump, sorted by name.
func (t *Trunks) Stumps() []StumpInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	var out []StumpInfo
	for key, n := range t.idx.All() {
		if n.Kind != "loadout" {
			continue
		}
		// Range the key: a loadout node is named by its key, and recovering
		// it from stumpName() returns "" for an empty Branch, which then
		// asks for the ROOT's children.
		si := StumpInfo{Name: key}
		for _, ck := range t.idx.ChildrenOf(key) {
			if c := t.node(ck); c != nil && c.Trunk != "" {
				si.Children = append(si.Children, c.Trunk)
			}
		}
		sort.Strings(si.Children)
		out = append(out, si)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// anchorOf returns the node where children of a trunk attach: its live
// head if it has one, else its (single, frozen) ceremonial node.
func (t *Trunks) anchorOf(trunk TrunkID) (string, bool) {
	if k, ok := t.idx.Head(trunk); ok {
		return k, true
	}
	for k, n := range t.idx.All() {
		if n.Trunk == trunk {
			return k, true
		}
	}
	return "", false
}

// AppendChannel appends to a related channel of a trunk's head.
func (t *Trunks) AppendChannel(trunk, channel string, mainLT uint64, payload, meta []byte) (uint64, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return 0, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return 0, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return 0, err
	}
	defer release()
	if mainLT == 0 {
		mainLT = mainTail(x) + 1 // reducible default: one ahead
	}
	return x.Append(channel, mainLT, payload, meta)
}

// LatestChannelRecord reads the newest channel checkpoint from the hot
// immutable snapshot when it is at or beyond minMainLT.
func (t *Trunks) LatestChannelRecord(trunk, channel string, minMainLT uint64) (Record, bool, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return Record{}, false, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return Record{}, false, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return Record{}, false, err
	}
	defer release()
	return x.LatestChannelRecord(channel, minMainLT)
}

// --- listing ---

// TrunkInfo is a read-only view of a trunk.
type TrunkInfo struct {
	ID         string
	Head       []string // head node branch
	Parent     string   // parent trunk id ("" if rooted at a stump or the root)
	Stump      string   // stump name, if the trunk is rooted directly at a stump
	Kind       string   // the founding node's kind, from its marker ("conversation", "form", …)
	BranchedLT uint64
	Tip        uint64
}

// List returns every trunk, in id order.
func (t *Trunks) List() []TrunkInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	ids := t.idx.LiveTrunks()
	out := make([]TrunkInfo, 0, len(ids))
	for _, id := range ids {
		unlockLineage := t.lockLineage(id)
		key := t.head(id)
		ti := TrunkInfo{ID: id, Head: []string{key}}
		if n := t.node(key); n != nil {
			ti.Kind = n.Kind
		}
		ti.Parent, ti.Stump, ti.BranchedLT = t.lineage(id)
		if x, err := t.openHotTopology(key); err == nil {
			ti.Tip = mainTail(x)
			x.Close()
		}
		unlockLineage()
		out = append(out, ti)
	}
	return out
}

// lineage finds a trunk's founding node (shallowest node carrying the
// trunk), returning its parent trunk id, the stump it is rooted at (if its
// parent is a stump), and the LT it branched at.
func (t *Trunks) lineage(trunk string) (parent, stump string, bl uint64) {
	head := t.head(trunk)
	n := t.node(head)
	if n == nil || n.IsRoot {
		return "", "", 0
	}
	// Flat: one node per trunk, so the head IS the founding node and its
	// parent is From. The old climb walked a continuation chain that a flat
	// fork never builds.
	p := t.node(n.From)
	if p == nil {
		return "", "", 0
	}
	return p.Trunk, stumpName(n.From, p), t.forkBaseOf(head)
}

// forkBaseOf reads a node's main-channel .fork base (the LT it forked at)
// directly from the marker file — cheap, no log open / segment scan.
func (t *Trunks) forkBaseOf(key string) uint64 {
	b, err := os.ReadFile(filepath.Join(t.irDir(key), ".fork"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "base="); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// ListLight is List without Tip (the head's tail index). Tip requires opening
// the head's log (a segment scan); most callers (figaro's aria listing) never
// use it. ListLight opens no logs — ids, parent/stump lineage, and BranchedLT
// all come from the in-memory node tree + the cheap .fork marker read — so it
// is O(trunks) with no per-trunk disk scan. Tip is left zero; use Head/List
// when you actually need it.
func (t *Trunks) ListLight() []TrunkInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	out := make([]TrunkInfo, 0, len(t.idx.LiveTrunks()))
	for _, id := range t.idx.LiveTrunks() {
		key := t.head(id)
		ti := TrunkInfo{ID: id, Head: []string{key}}
		if n := t.node(key); n != nil {
			ti.Kind = n.Kind
		}
		ti.Parent, ti.Stump, ti.BranchedLT = t.lineage(id)
		out = append(out, ti)
	}
	return out
}

// Nodes returns every node (debug), root first. NodeInfo lives in
// index.go; Frozen is a method there, derived from Children.
func (t *Trunks) Nodes() map[string]NodeInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	out := map[string]NodeInfo{}
	for key, n := range t.idx.All() {
		out[key] = *n
	}
	return out
}

// --- helpers ---

func (t *Trunks) mintNode() string { return t.idx.MintNode() }

func (t *Trunks) mintTrunk(kind string) string { return t.idx.MintTrunk(kind) }

// irDir is a node's main-channel directory. A flat key is one path element;
// the root is the channel dir itself.
func (t *Trunks) irDir(key string) string {
	if key == "" {
		return filepath.Join(t.root, t.main)
	}
	return filepath.Join(t.root, t.main, fsName(key))
}

// numSuffix parses the numeric suffix of e.g. "n12" / "t3" given the
// expected prefix rune; returns -1 if it doesn't match.
func numSuffix(s string, prefix byte) int {
	if len(s) < 2 || s[0] != prefix {
		return -1
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil {
		return -1
	}
	return n
}

// readManifestFile is the read-only load, for the callers that want one
// field and must not create anything. loadOrCreateManifest is the writing
// path; this one is why there are no longer three hand-rolled readers.
func readManifestFile(dir string) (manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return manifest{}, fmt.Errorf("xwal: read manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("xwal: parse manifest: %w", err)
	}
	return m, nil
}

// channelNames reads the manifest and returns every channel's name (each
// maps directly to a dir under the root: "ir", "chalkboard",
// "translations/<provider>", …).
func channelNames(dir string) ([]string, error) {
	m, err := readManifestFile(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m.Channels))
	for _, c := range m.Channels {
		out = append(out, c.Name)
	}
	return out, nil
}

// mainChannelName reads the manifest to find the main channel name.
func mainChannelName(dir string) (string, error) {
	m, err := readManifestFile(dir)
	if err != nil {
		return "", err
	}
	return m.Main, nil
}

// ownFirstIdx is the head's own first main index (forkBase, or 1 at root).
func ownFirstIdx(x *XWAL) uint64 {
	if fb := x.chans[x.main].log.ForkBase(); fb > 0 {
		return fb
	}
	return 1
}

// Kind reports a trunk's node kind ("conversation", "form", "loadout", …)
// from the in-memory index — no log opened. The founding node carries it
// (flat layout: one node per trunk), so this is the cheap discriminator
// consumers split listings and birth verbs on.
func (t *Trunks) Kind(trunk TrunkID) (string, bool) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return "", false
	}
	defer endRead()
	key, ok := t.anchorOf(trunk)
	if !ok {
		return "", false
	}
	n := t.node(key)
	if n == nil {
		return "", false
	}
	return n.Kind, true
}

// SyncChannelThrough persists one channel of one trunk up to idx. The
// borrow-and-release dance is the same one Append performs, so a writer can
// append a batch and then make exactly that batch durable.
func (t *Trunks) SyncChannelThrough(trunk, channel string, idx uint64) error {
	x, err := t.Head(trunk)
	if err != nil {
		return err
	}
	defer x.Close()
	return x.SyncChannelThrough(channel, idx)
}
