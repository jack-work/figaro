package xwal

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
)

// Trunks is the trunk-addressed view: a trunk is one continuation chain
// across all channels, the stable handle a caller addresses; the per-fork
// node is plumbing.
type Trunks struct {
	root string
	cfg  Config
	main string

	registryRoot string

	mu  sync.RWMutex
	idx *Index

	// version is the local rebuild cookie; rootEpoch tracks mutations made
	// through any in-process peer for the same canonical root.
	rootEpoch atomic.Uint64

	hotMu   sync.Mutex
	hot     *trunkStore
	retired map[*trunkStore]struct{}

	// ltsReg retains each node's lastTS counter across open/close/evict cycles.
	ltsReg *lastTSRegistry

	validationMu             sync.Mutex
	validationGeneration     uint64
	validatedTopologyVersion uint64
	validatedForkBranches    map[string]uint64

	testAfterReadLock func()
	testDeepRepair    func()
}

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

func mainTail(x *XWAL) uint64 {
	l, err := x.channel(x.main).Log()
	if err != nil {
		return 0
	}
	return l.LastIndex()
}

// createTrunks seeds the genesis at the root. The root is the channel
// directory itself: it carries no .trunk marker and holds the genesis every
// trunk inherits.
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
	t.cfg.probeTS = t.probeLastTS
	t.cfg.ParentOf = t.idx.ParentOf
	t.cfg.channelGen = channelGenFor(t.registryRoot)
	if err := t.rebuild(); err != nil {
		return nil, err
	}
	t.markTopologyValidated()
	if err := t.register(); err != nil {
		return nil, err
	}
	return t, nil
}

func settleManifest(dir, root string, cfg Config) (manifest, error) {
	state := rootTopologyStateFor(root)
	state.manifestMu.Lock()
	defer state.manifestMu.Unlock()
	man, err := loadOrCreateManifest(dir, cfg)
	if err != nil {
		return manifest{}, err
	}
	// Before anything walks: a nested v3 store opens "successfully" from here
	// and reports its stumps and none of its conversations, because
	// RebuildFrom reads one directory. Either the store is this build's
	// layout or it is refused; Flatten is the way through.
	switch {
	case man.Layout > layoutVersion:
		return manifest{}, fmt.Errorf("%w: store %s is layout %d, this build reads %d",
			ErrFutureLayout, dir, man.Layout, layoutVersion)
	case man.Layout < layoutVersion:
		return manifest{}, fmt.Errorf("%w: store %s is layout %d, this build writes %d",
			ErrLegacyLayout, dir, man.Layout, layoutVersion)
	}
	man, err = recoverChannelPending(dir, cfg, man)
	if err != nil {
		return manifest{}, err
	}
	if man, err = reconcileChannelProps(dir, cfg, man); err != nil {
		return manifest{}, err
	}
	return materializeManifestChannels(dir, cfg, man)
}

func openTrunks(dir string, cfg Config) (*Trunks, error) {
	endMutation, root, err := beginRootTopologyMutation(dir)
	if err != nil {
		return nil, err
	}
	defer endMutation()
	retireTrunkStores(dir)
	man, err := settleManifest(dir, root, cfg)
	if err != nil {
		return nil, err
	}
	if err := (&Trunks{root: dir, main: man.Main}).resumeGrafts(); err != nil {
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
	t.cfg.probeTS = t.probeLastTS
	t.cfg.ParentOf = t.idx.ParentOf
	t.cfg.channelGen = channelGenFor(t.registryRoot)
	if err := t.rebuild(); err != nil {
		return nil, err
	}
	t.markTopologyValidated()
	if err := t.register(); err != nil {
		return nil, err
	}
	return t, nil
}

// node returns nil when absent.
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

// Refresh re-scans the markers: the escape hatch for a store another process mutated.
func (t *Trunks) Refresh() error {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return err
	}
	defer endMutation()
	t.invalidateTopologyValidation()
	return t.rebuild()
}

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

// Close releases cached segment handles; every Head or StumpHead must be
// closed first. The topology cache remains usable.
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

func (t *Trunks) ensureChannel(spec ChannelSpec) error {
	return registerChannel(t.root, t.cfg, spec)
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

// ownFirstIdx is the head's own first main index (forkBase, or 1 at root).
func ownFirstIdx(x *XWAL) uint64 {
	l, err := x.channel(x.main).Log()
	if err != nil {
		return 1
	}
	if fb := l.ForkBase(); fb > 0 {
		return fb
	}
	return 1
}
