package xwal

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type rootTopologyState struct {
	mu         sync.Mutex
	ready      *sync.Cond
	mutating   bool
	borrowers  int
	epoch      uint64
	owners     map[*Trunks]int
	lineages   map[string]*rootLineageState
	manifestMu sync.Mutex
	channelGen atomic.Uint64
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

// topologyWaitTimeout bounds how long topology mutations wait for open heads.
var topologyWaitTimeout = 3 * time.Second

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

// beginFlatCreate: a flat fork writes only its own new sibling directory and
// one index entry, never freezing or rewriting anything a live head reads,
// so it takes no root topology mutation and does not wait for other arias to
// close their heads.
func (t *Trunks) beginFlatCreate() (func(), error) {
	t.mu.Lock()
	if err := t.syncHot(); err != nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("xwal: refusing create, hot flush failing: %w", err)
	}
	// A peer moved the tree: re-read it, or we fork from a node we do not know.
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
		// A failing flush must fail the topology op loudly: retiring the hot
		// store would silently drop the unflushed (acked) tail.
		if err := t.syncHot(); err != nil {
			end()
			t.mu.Unlock()
			return nil, fmt.Errorf("xwal: refusing topology mutation, hot flush failing: %w", err)
		}
		// Re-read the tree only when a peer moved it: end() stamps the new
		// epoch, so after our own mutation we are already current.
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
