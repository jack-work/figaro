package xwal

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/store/log"
)

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

// syncHot flushes every loaded head lineage-coherently (main first, related
// channels capped at durable main referents), so the stray sweep can never
// push a related record ahead of its main entry.
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
