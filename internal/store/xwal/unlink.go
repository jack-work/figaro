package xwal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/store/disk"
)

type pendingRemoval struct {
	plan   string
	c      Component
	keys   map[string]bool
	since  time.Time
	warned bool
}

type PendingRemoval struct {
	Nodes []string
	Since time.Time
	Heads int
}

var (
	removalWarnAfter atomic.Int64
	drainTick        = 25 * time.Millisecond
)

func init() { removalWarnAfter.Store(int64(time.Minute)) }

func removalToken() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (t *Trunks) step(name string) {
	if t.testRemovalStep != nil {
		t.testRemovalStep(name)
	}
}

func (s *rootTopologyState) deadLocked(branch []string) bool {
	for _, key := range branch {
		if _, ok := s.dead[key]; ok {
			return true
		}
	}
	return false
}

func (t *Trunks) isDead(branch []string) bool {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.deadLocked(branch)
}

func (t *Trunks) reviveNode(key string) {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	delete(state.dead, key)
	state.mu.Unlock()
}

func (t *Trunks) markDead(keys []string) {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	defer state.mu.Unlock()
	for {
		busy := false
		for _, k := range keys {
			if state.cutting[k] > 0 {
				busy = true
				break
			}
		}
		if !busy {
			break
		}
		state.ready.Wait()
	}
	if state.dead == nil {
		state.dead = map[string]struct{}{}
	}
	for _, k := range keys {
		state.dead[k] = struct{}{}
	}
}

func (t *Trunks) unmarkDead(keys []string) {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	for _, k := range keys {
		delete(state.dead, k)
	}
	state.mu.Unlock()
}

func (t *Trunks) beginCut(node string) (func(), error) {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, dead := state.dead[node]; dead {
		return nil, fmt.Errorf("%w: node %s is being removed", ErrUnknownTrunk, node)
	}
	if state.cutting == nil {
		state.cutting = map[string]int{}
	}
	state.cutting[node]++
	return func() {
		state.mu.Lock()
		state.cutting[node]--
		if state.cutting[node] == 0 {
			delete(state.cutting, node)
		}
		state.ready.Broadcast()
		state.mu.Unlock()
	}, nil
}

func (t *Trunks) componentOfLocked(keys []string) Component {
	c := Component{Main: t.main}
	for _, k := range keys {
		n := t.node(k)
		if n == nil {
			continue
		}
		c.Nodes = append(c.Nodes, ComponentNode{Key: k, From: n.From, Kind: n.Kind, Trunk: n.Trunk})
	}
	return c
}

func (t *Trunks) commitRemoval(c Component, token string) error {
	keys := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		keys = append(keys, n.Key)
	}
	t.markDead(keys)
	dir := filepath.Join(t.root, removalPrefix+token)
	abandon := func(err error) error {
		t.unmarkDead(keys)
		_ = os.RemoveAll(dir)
		return err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return abandon(err)
	}
	t.step("plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return abandon(err)
	}
	if err := writeSyncedFile(filepath.Join(dir, graftPlanName), body); err != nil {
		return abandon(err)
	}
	t.step("sync-plan-file")
	if err := disk.SyncDir(dir); err != nil {
		return abandon(err)
	}
	t.step("sync-plan-dir")
	if err := disk.SyncDir(t.root); err != nil {
		return abandon(err)
	}
	t.step("sync-root")
	for i := len(c.Nodes) - 1; i >= 0; i-- {
		err := os.Remove(filepath.Join(t.irDir(c.Nodes[i].Key), nodeMarkerName))
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("xwal: removal %s committed, marker unlink failed (finishes at next open): %w", token, err)
		}
		t.step("unlink-marker")
	}
	if err := disk.SyncDir(t.irDir("")); err != nil {
		return fmt.Errorf("xwal: removal %s committed, main dir sync failed (finishes at next open): %w", token, err)
	}
	t.step("markers")
	t.idx.Drop(keys)
	t.publishEpoch()
	state := rootTopologyStateFor(t.registryRoot)
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	state.mu.Lock()
	state.pending = append(state.pending, &pendingRemoval{plan: dir, c: c, keys: set, since: time.Now()})
	state.mu.Unlock()
	t.step("logical")
	return nil
}

func (t *Trunks) publishEpoch() {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	state.epoch++
	t.rootEpoch.Store(state.epoch)
	state.ready.Broadcast()
	state.mu.Unlock()
}

func peersOf(root string) []*Trunks {
	trunkRegistry.Lock()
	defer trunkRegistry.Unlock()
	out := make([]*Trunks, 0, len(trunkRegistry.roots[root]))
	for peer := range trunkRegistry.roots[root] {
		out = append(out, peer)
	}
	return out
}

func (t *Trunks) storesLocked() []*trunkStore {
	stores := make([]*trunkStore, 0, 1+len(t.retired))
	if t.hot != nil {
		stores = append(stores, t.hot)
	}
	for h := range t.retired {
		stores = append(stores, h)
	}
	return stores
}

func readsAny(key string, dead map[string]bool) bool {
	for _, k := range strings.Split(key, "\x00") {
		if dead[k] {
			return true
		}
	}
	return false
}

func holdPrivate(root string, branch []string) func() {
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	if state.private == nil {
		state.private = map[string]int{}
	}
	for _, k := range branch {
		state.private[k]++
	}
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		for _, k := range branch {
			state.private[k]--
			if state.private[k] == 0 {
				delete(state.private, k)
			}
		}
		state.mu.Unlock()
	}
}

func headsReading(root string, dead map[string]bool) int {
	refs := 0
	state := rootTopologyStateFor(root)
	state.mu.Lock()
	for k := range dead {
		refs += state.private[k]
	}
	state.mu.Unlock()
	for _, peer := range peersOf(root) {
		peer.hotMu.Lock()
		for _, h := range peer.storesLocked() {
			for key, head := range h.heads {
				if readsAny(key, dead) {
					refs += head.refs
				}
			}
		}
		peer.hotMu.Unlock()
	}
	return refs
}

func (t *Trunks) unloadDead(p *pendingRemoval) (bool, error) {
	names, err := channelNames(t.root)
	if err != nil {
		return false, err
	}
	var dirs []string
	for _, n := range p.c.Nodes {
		for _, ch := range names {
			dirs = append(dirs, filepath.Join(t.root, ch, fsName(n.Key)))
		}
	}
	peers := peersOf(t.registryRoot)
	for _, peer := range peers {
		peer.hotMu.Lock()
	}
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	busy := false
	for k := range p.keys {
		if state.private[k] != 0 {
			busy = true
		}
	}
	state.mu.Unlock()
	for _, peer := range peers {
		for _, h := range peer.storesLocked() {
			for key, head := range h.heads {
				if readsAny(key, p.keys) && head.refs != 0 {
					busy = true
				}
			}
		}
	}
	var stores []*trunkStore
	if !busy {
		for _, peer := range peers {
			for _, h := range peer.storesLocked() {
				for key := range h.heads {
					if readsAny(key, p.keys) {
						delete(h.heads, key)
					}
				}
				stores = append(stores, h)
			}
		}
	}
	for _, peer := range peers {
		peer.hotMu.Unlock()
	}
	if busy {
		return false, nil
	}
	for _, h := range stores {
		for _, d := range dirs {
			if err := h.store.Forget(d); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func (t *Trunks) finishRemoval(c Component, plan string) error {
	names, err := channelNames(t.root)
	if err != nil {
		return err
	}
	for i := len(c.Nodes) - 1; i >= 0; i-- {
		if err := os.Remove(filepath.Join(t.irDir(c.Nodes[i].Key), nodeMarkerName)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, ch := range names {
		for _, n := range c.Nodes {
			if err := os.RemoveAll(filepath.Join(t.root, ch, fsName(n.Key))); err != nil {
				return err
			}
		}
		t.step("unlink-channel")
	}
	if err := disk.SyncDir(t.irDir("")); err != nil {
		return err
	}
	t.step("dirs")
	return os.RemoveAll(plan)
}

func (t *Trunks) drainRemovals() bool {
	state := rootTopologyStateFor(t.registryRoot)
	state.drainMu.Lock()
	defer state.drainMu.Unlock()
	state.mu.Lock()
	queue := append([]*pendingRemoval(nil), state.pending...)
	state.mu.Unlock()

	done := map[*pendingRemoval]bool{}
	for _, p := range queue {
		if refs := headsReading(t.registryRoot, p.keys); refs > 0 {
			if !p.warned && time.Since(p.since) > time.Duration(removalWarnAfter.Load()) {
				p.warned = true
				slog.Warn("xwal: a removed node is still read; its directories wait",
					"nodes", sortedKeys(p.keys), "heads", refs, "since", p.since)
			}
			continue
		}
		ok, err := t.unloadDead(p)
		if err != nil {
			slog.Warn("xwal: unloading a removed node", "nodes", sortedKeys(p.keys), "err", err)
			continue
		}
		if !ok {
			continue
		}
		if err := t.finishRemoval(p.c, p.plan); err != nil {
			slog.Warn("xwal: unlinking a removed node (finishes at next open)", "nodes", sortedKeys(p.keys), "err", err)
			continue
		}
		done[p] = true
	}

	state.mu.Lock()
	kept := state.pending[:0]
	for _, p := range state.pending {
		if !done[p] {
			kept = append(kept, p)
		}
	}
	state.pending = kept
	left := len(state.pending) > 0
	start := left && !state.drainerRunning
	if start {
		state.drainerRunning = true
	}
	state.mu.Unlock()
	if start {
		go t.drainLoop()
	}
	return left
}

func (t *Trunks) abandonRemovals() {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	state.pending = nil
	state.mu.Unlock()
}

func (t *Trunks) drainLoop() {
	tick := drainTick
	for {
		time.Sleep(tick)
		if len(peersOf(t.registryRoot)) == 0 || !t.drainRemovals() {
			break
		}
		if tick < 500*time.Millisecond {
			tick *= 2
		}
	}
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	state.drainerRunning = false
	again := len(state.pending) > 0
	if again {
		state.drainerRunning = true
	}
	state.mu.Unlock()
	if again {
		go t.drainLoop()
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (t *Trunks) PendingRemovals() []PendingRemoval {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	pending := append([]*pendingRemoval(nil), state.pending...)
	state.mu.Unlock()
	out := make([]PendingRemoval, 0, len(pending))
	for _, p := range pending {
		out = append(out, PendingRemoval{Nodes: sortedKeys(p.keys), Since: p.since, Heads: headsReading(t.registryRoot, p.keys)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

func (t *Trunks) removalPending(key string) bool {
	state := rootTopologyStateFor(t.registryRoot)
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, p := range state.pending {
		if p.keys[key] {
			return true
		}
	}
	return false
}
