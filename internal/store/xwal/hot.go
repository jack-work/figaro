package xwal

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/store/log"
)

type trunkStore struct {
	store     *log.Store
	heads     map[string]*hotHead
	refs      int
	retired   bool
	unflushed map[string]error
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
	key := strings.Join(branch, "\x00")
	if holder := rootFenceHolder(t.registryRoot, key); holder != nil && holder != t {
		if err := holder.retryFlush(key); err != nil {
			return nil, nil, err
		}
	}
	t.hotMu.Lock()
	for {
		fence := t.fenceOverLocked(key)
		if fence == nil {
			break
		}
		t.hotMu.Unlock()
		<-fence
		t.hotMu.Lock()
	}
	if t.isDead(branch) {
		t.hotMu.Unlock()
		return nil, nil, fmt.Errorf("%w: node %s is removed", ErrUnknownTrunk, key)
	}
	if err := t.unflushedLocked(key); err != nil {
		t.hotMu.Unlock()
		return nil, nil, err
	}
	h := t.hot
	if h == nil {
		h = &trunkStore{store: log.NewStore(), heads: map[string]*hotHead{}}
		t.hot = h
	}
	h.refs++
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
// under the lineage lock, then the head and its channel snapshots are
// dropped. A head still borrowed is skipped. Reports whether the lineage is
// now unloaded.
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

	for _, name := range head.x.channels().order {
		l := head.x.channel(name).opened()
		if l == nil {
			continue
		}
		if err := l.Sync(); err != nil {
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
		for _, ch := range other.x.channels().byName {
			inUse[ch.dir] = true
		}
	}
	t.hotMu.Unlock()

	for _, name := range head.x.channels().order {
		ch := head.x.channel(name)
		if inUse[ch.dir] {
			continue
		}
		if err := h.store.Evict(ch.dir); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (t *Trunks) forgetLineageHead(trunk string) bool {
	t.mu.RLock()
	key, ok := t.idx.Head(trunk)
	t.mu.RUnlock()
	if !ok {
		return false
	}
	names, err := channelNames(t.root)
	if err != nil {
		return false
	}
	t.hotMu.Lock()
	for r := range t.retired {
		if head := r.heads[key]; head != nil && head.refs != 0 {
			t.hotMu.Unlock()
			return false
		}
	}
	h := t.hot
	if h == nil {
		t.hotMu.Unlock()
		return true
	}
	head := h.heads[key]
	if head != nil && head.refs != 0 {
		t.hotMu.Unlock()
		return false
	}
	h.refs++
	t.hotMu.Unlock()
	defer func() { _ = t.releaseHot(h) }()
	if head != nil {
		select {
		case <-head.ready:
		default:
			return false
		}
		if head.x != nil && head.err == nil {
			if err := t.syncHead(key, head.x); err != nil {
				return false
			}
		}
		t.hotMu.Lock()
		if h.heads[key] != head || head.refs != 0 {
			t.hotMu.Unlock()
			return false
		}
		delete(h.heads, key)
		t.hotMu.Unlock()
	}
	for _, ch := range names {
		dir := filepath.Join(t.root, ch, fsName(key))
		if !h.store.Holds(dir) {
			continue
		}
		if err := h.store.Forget(dir); err != nil {
			return false
		}
	}
	return true
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

func (t *Trunks) releaseHot(h *trunkStore) error {
	t.hotMu.Lock()
	h.refs--
	if h.retired && h.refs == 0 {
		t.flushStoreLocked(h)
	}
	closeStore := h.retired && h.refs == 0 && len(h.unflushed) == 0
	if closeStore {
		delete(t.retired, h)
	}
	t.hotMu.Unlock()
	if closeStore {
		return t.closeStore(h)
	}
	return nil
}

// closeStore is the close that flushes a store's buffered tail; every close of
// a hot or retired store goes through it.
func (t *Trunks) closeStore(h *trunkStore) error {
	if t.testStoreClose != nil {
		t.testStoreClose()
	}
	return h.store.Close()
}

// syncHot flushes every loaded head lineage-coherently (main first, related
// channels capped at durable main referents), so the stray sweep can never
// push a related record ahead of its main entry.
func (t *Trunks) syncHot() error {
	t.hotMu.Lock()
	h := t.hot
	if h != nil {
		h.refs++
	}
	heads := map[string]*hotHead{}
	if h != nil {
		for key, head := range h.heads {
			heads[key] = head
			head.refs++
		}
	}
	t.hotMu.Unlock()
	if h == nil {
		return nil
	}
	var err error
	for key, head := range heads {
		select {
		case <-head.ready:
		default:
			continue
		}
		if head.x == nil || head.err != nil {
			continue
		}
		if ferr := t.syncHead(key, head.x); ferr != nil && err == nil {
			node := head.x.key
			if node == "" {
				node = "root"
			}
			err = fmt.Errorf("node %s: %w", node, ferr)
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

// retireHot is the one place a hot store stops being hot, and every
// retirement path comes here. Before any other borrow can make a new store,
// the old store's buffered tail is on disk: closed now if nothing borrows it,
// synced now if something does.
// retireHot is the one place a hot store stops being hot, and every
// retirement path comes here. Before any other borrow can make a new store,
// the old store's buffered tail is on disk: every loaded head is synced, and
// the store closed if nothing borrows it. A head whose sync fails keeps the
// store retired and open, and its node refuses new heads until a retry
// flushes it.
func (t *Trunks) retireHot() {
	t.hotMu.Lock()
	defer t.hotMu.Unlock()
	h := t.hot
	t.hot = nil
	if h == nil {
		return
	}
	h.retired = true
	t.flushStoreLocked(h)
	if h.refs == 0 && len(h.unflushed) == 0 {
		if err := t.closeStore(h); err != nil {
			slog.Warn("xwal: closing a retired hot store", "err", err)
		}
		return
	}
	if t.retired == nil {
		t.retired = map[*trunkStore]struct{}{}
	}
	t.retired[h] = struct{}{}
}

func (t *Trunks) syncHead(key string, x *XWAL) error {
	if t.testHeadSync != nil {
		if err := t.testHeadSync(key); err != nil {
			return err
		}
	}
	return x.syncCoherent()
}

func (t *Trunks) flushStoreLocked(h *trunkStore) {
	for key, head := range h.heads {
		select {
		case <-head.ready:
		default:
			continue
		}
		if head.x == nil || head.err != nil {
			continue
		}
		if err := t.syncHead(key, head.x); err != nil {
			slog.Error("xwal: a retired hot store's tail could not be flushed; its node refuses new heads until it is", "node", head.x.key, "err", err)
			if h.unflushed == nil {
				h.unflushed = map[string]error{}
			}
			h.unflushed[key] = err
			t.fenceRoot(key, true)
			continue
		}
		if _, was := h.unflushed[key]; was {
			delete(h.unflushed, key)
			t.fenceRoot(key, false)
		}
	}
}

// unflushedLocked retries the flush of any retired store still holding an
// unflushed tail of key, and reports the error if one remains. A store that
// is flushed and unborrowed is closed here.
func (t *Trunks) unflushedLocked(key string) error {
	held := false
	defer func() {
		if !held {
			t.fenceRoot(key, false)
		}
	}()
	for h := range t.retired {
		if _, ok := h.unflushed[key]; !ok {
			continue
		}
		held = true
		t.flushStoreLocked(h)
		if err, still := h.unflushed[key]; still {
			return fmt.Errorf("xwal: node %s holds entries a retired store could not flush: %w", key, err)
		}
		if len(h.unflushed) == 0 && h.refs == 0 {
			delete(t.retired, h)
			if err := t.closeStore(h); err != nil {
				slog.Warn("xwal: closing a retired hot store", "err", err)
			}
		}
	}
	return nil
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

// retireRootHot drops the root topology head: every flat fork opens it to
// read the parent's channel tails, and it belongs to no trunk, so lineage
// eviction can never reach it.
func (t *Trunks) retireRootHot() {
	invalidateRootTopologyValidation(t.registryRoot)
	retireTrunkStores(t.root)
}

func (t *Trunks) retireRootHotPreservingValidation() {
	retireTrunkStores(t.root)
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

func (t *Trunks) descendsFrom(key, node string) bool {
	if key == node {
		return true
	}
	if t.cfg.ParentOf == nil || key == "" || strings.Contains(key, "\x00") {
		return false
	}
	for hops, k := t.idx.Len(), t.cfg.ParentOf(key); k != "" && hops >= 0; k, hops = t.cfg.ParentOf(k), hops-1 {
		if k == node {
			return true
		}
	}
	return false
}

func (t *Trunks) fenceOverLocked(key string) chan struct{} {
	for node, fence := range t.fences {
		if t.descendsFrom(key, node) {
			return fence
		}
	}
	return nil
}

func (t *Trunks) fenceAlongLocked(node string) chan struct{} {
	for other, fence := range t.fences {
		if t.descendsFrom(node, other) || t.descendsFrom(other, node) {
			return fence
		}
	}
	return nil
}

func (t *Trunks) busyReadersOfLocked(node string) int {
	refs := 0
	stores := make([]*trunkStore, 0, 1+len(t.retired))
	if t.hot != nil {
		stores = append(stores, t.hot)
	}
	for h := range t.retired {
		stores = append(stores, h)
	}
	for _, h := range stores {
		for key, head := range h.heads {
			if t.descendsFrom(key, node) {
				refs += head.refs
			}
		}
	}
	return refs
}

// fenceNode stops new borrows of a node and of every node that reads it as
// its flat parent, waits for the borrows already out to return, and unloads
// those heads with their buffers flushed. Heads of unrelated arias are left
// alone: a cut in one aria's channel never waits on another aria's reads.
func (t *Trunks) fenceNode(node string) (func(), error) {
	t.hotMu.Lock()
	for {
		fence := t.fenceAlongLocked(node)
		if fence == nil {
			break
		}
		t.hotMu.Unlock()
		<-fence
		t.hotMu.Lock()
	}
	if t.fences == nil {
		t.fences = map[string]chan struct{}{}
	}
	fence := make(chan struct{})
	t.fences[node] = fence
	t.hotMu.Unlock()
	release := func() {
		t.hotMu.Lock()
		if t.fences[node] == fence {
			delete(t.fences, node)
		}
		t.hotMu.Unlock()
		close(fence)
	}

	deadline := time.Now().Add(topologyWaitTimeout)
	for {
		t.hotMu.Lock()
		refs := t.busyReadersOfLocked(node)
		t.hotMu.Unlock()
		if refs == 0 {
			break
		}
		if time.Now().After(deadline) {
			release()
			return nil, fmt.Errorf("xwal: channel cut timed out waiting for %d open head(s) on node %s", refs, node)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := t.unloadReadersOf(node); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (t *Trunks) unloadReadersOf(node string) error {
	t.hotMu.Lock()
	var stale []*hotHead
	stores := make([]*trunkStore, 0, 1+len(t.retired))
	if t.hot != nil {
		stores = append(stores, t.hot)
	}
	for h := range t.retired {
		stores = append(stores, h)
	}
	for _, h := range stores {
		for key, head := range h.heads {
			if t.descendsFrom(key, node) && head.x != nil && head.err == nil {
				stale = append(stale, head)
			}
		}
	}
	t.hotMu.Unlock()
	for _, head := range stale {
		for _, name := range head.x.channels().order {
			if l := head.x.channel(name).opened(); l != nil {
				if err := l.Sync(); err != nil {
					return err
				}
			}
		}
	}

	t.hotMu.Lock()
	h := t.hot
	var dirs []string
	if h != nil {
		for key, head := range h.heads {
			if !t.descendsFrom(key, node) {
				continue
			}
			delete(h.heads, key)
			if head.x == nil {
				continue
			}
			for _, name := range head.x.channels().order {
				dirs = append(dirs, head.x.channel(name).dir)
			}
		}
	}
	t.hotMu.Unlock()
	if h == nil {
		return nil
	}
	for _, dir := range dirs {
		if err := h.store.Evict(dir); err != nil {
			return err
		}
	}
	return nil
}

// cutChannel runs a destructive channel edit on a trunk's head node with only
// that node and its readers fenced: the lineage lock keeps writers out, the
// fence keeps readers out, and every cached handle on the node is gone before
// the private open edits the files.
func (t *Trunks) cutChannel(trunk, channel string, edit func(*XWAL) error) error {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	t.mu.RLock()
	node, err := t.headKey(trunk)
	t.mu.RUnlock()
	if err != nil {
		return err
	}
	endCut, err := t.beginCut(node)
	if err != nil {
		return err
	}
	defer endCut()
	release, err := t.fenceNode(node)
	if err != nil {
		return err
	}
	defer release()
	x, err := Open(t.root, t.cfg, node)
	if err != nil {
		return err
	}
	x.retire = func() {}
	if ch := x.channel(channel); ch != nil {
		t.hotMu.Lock()
		stores := make([]*trunkStore, 0, 1+len(t.retired))
		if t.hot != nil {
			stores = append(stores, t.hot)
		}
		for h := range t.retired {
			stores = append(stores, h)
		}
		t.hotMu.Unlock()
		for _, h := range stores {
			if err := h.store.Forget(ch.dir); err != nil {
				_ = x.Close()
				return err
			}
		}
	}
	editErr := edit(x)
	if cerr := x.Close(); editErr == nil {
		editErr = cerr
	}
	return editErr
}

// fenceRoot records, for every Trunks on the root, that this one holds the
// node's tail unflushed; a peer's borrow of the node asks this one to retry.
func (t *Trunks) fenceRoot(key string, on bool) {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	defer state.mu.Unlock()
	if on {
		if state.unflushed == nil {
			state.unflushed = map[string]*Trunks{}
		}
		state.unflushed[key] = t
		return
	}
	if state.unflushed[key] == t {
		delete(state.unflushed, key)
	}
}

func rootFenceHolder(root, key string) *Trunks {
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.unflushed[key]
}

func (t *Trunks) retryFlush(key string) error {
	t.hotMu.Lock()
	defer t.hotMu.Unlock()
	return t.unflushedLocked(key)
}
