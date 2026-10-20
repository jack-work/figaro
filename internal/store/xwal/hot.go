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
