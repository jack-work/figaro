package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/topo"
)

// topologyStump cannot collide with an outfit stump: those are "@" + a hex hash.
const topologyStump = "@topology"

type stumpFormLog struct {
	store *XwalStore
	stump string
}

func (l *stumpFormLog) AppendPatch(payload []byte) (uint64, error) {
	x, err := l.store.trunks.StumpHead(l.stump)
	if err != nil {
		return 0, err
	}
	defer x.Close()
	return x.Append(chanForm, 0, payload, nil)
}

func (l *stumpFormLog) SyncThrough(index uint64) error {
	x, err := l.store.trunks.StumpHead(l.stump)
	if err != nil {
		return err
	}
	defer x.Close()
	return x.SyncChannelThrough(chanForm, index)
}

func (l *stumpFormLog) DiscardFrom(index uint64) error {
	x, err := l.store.trunks.StumpHead(l.stump)
	if err != nil {
		return err
	}
	defer x.Close()
	return x.DiscardFrom(chanForm, index)
}

func (l *stumpFormLog) RangePatches(from, upTo uint64, fn func(uint64, []byte) error) error {
	x, err := l.store.trunks.StumpHead(l.stump)
	if err != nil {
		return err
	}
	defer x.Close()
	first, last, _ := x.ChannelBounds(chanForm)
	if first == 0 && last > 0 {
		first = 1
	}
	if from > first {
		first = from
	}
	if upTo > 0 && upTo < last {
		last = upTo
	}
	for lt := first; lt >= 1 && lt <= last; lt++ {
		rec, err := x.ReadAt(chanForm, lt)
		if err != nil {
			return err
		}
		if err := fn(lt, rec.Payload); err != nil {
			return err
		}
	}
	return nil
}

// TopologyTree: every mutator is one patch, so a promote's two edges land
// together or not at all.
type TopologyTree struct {
	form *Form
	topo topo.Topology
	// forks is the history adjacency: a delete repairs what reads through it.
	forks topo.Topology
}

func OpenTopologyTree(s *XwalStore, dir string) (*TopologyTree, error) {
	if err := s.ensureTopologyStump(); err != nil {
		return nil, err
	}
	f, err := OpenForm(&stumpFormLog{store: s, stump: topologyStump})
	if err != nil {
		return nil, err
	}
	t := &TopologyTree{form: f, topo: s.TopologyAdjacency(), forks: s.ForkAdjacency()}
	if err := t.migrate(filepath.Join(dir, "trunks.json")); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *XwalStore) ensureTopologyStump() error { return s.ensureStump(topologyStump) }

// Reserved stumps give a form a well-known id: figwal names a stump by a
// caller-chosen string, where every other node gets a system id.
func (s *XwalStore) ensureStump(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.trunks.Stumps() {
		if st.Name == name {
			return nil
		}
	}
	if err := s.trunks.CreateStump(name); err != nil {
		return fmt.Errorf("xwal store: create stump %q: %w", name, err)
	}
	return nil
}

func (t *TopologyTree) migrate(path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var s struct {
		Version int               `json:"version"`
		Parent  map[string]string `json:"parent,omitempty"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("topology: parse %s: %w", path, err)
	}
	if len(s.Parent) > 0 && len(t.Edges()) == 0 {
		set := make(map[string]json.RawMessage, len(s.Parent))
		for id, parent := range s.Parent {
			raw, err := json.Marshal(parent)
			if err != nil {
				return err
			}
			set[id] = raw
		}
		if _, _, err := t.form.ApplyEffectPrivileged(form.Build(form.Snapshot{}, set, nil), 0); err != nil {
			return fmt.Errorf("topology: migrate %s: %w", path, err)
		}
	}
	return os.Rename(path, path+".migrated")
}

func (t *TopologyTree) Close() { t.form.Close() }

func (t *TopologyTree) Edges() map[string]string {
	snap, _ := t.form.Snapshot()
	return edgesOf(snap)
}

func edgesOf(snap form.Snapshot) map[string]string {
	out := map[string]string{}
	for k := range snap.All() {
		if v := snap.Lookup(k); v != nil && *v != "" {
			out[k] = *v
		}
	}
	return out
}

func (t *TopologyTree) Prune(judge func(edges map[string]string) []topo.Stale, dryRun bool) ([]topo.Stale, uint64, error) {
	snap, version := t.form.Snapshot()
	stale := judge(edgesOf(snap))
	if dryRun || len(stale) == 0 {
		return stale, version, nil
	}
	remove := make([]string, len(stale))
	for i, st := range stale {
		remove[i] = st.ID
	}
	landed, _, err := t.form.applyEffect(form.Build(snap, nil, remove), version, Ensure, true)
	if err != nil {
		return nil, version, err
	}
	return stale, landed, nil
}

// Rev is the form's version: it moves on every landed edit.
func (t *TopologyTree) Rev() uint64 { return t.form.Read().Version }

func (t *TopologyTree) Parent(id string) (string, bool) {
	snap, _ := t.form.Snapshot()
	if v := snap.Lookup(id); v != nil && *v != "" {
		return *v, true
	}
	return t.topo.From(id)
}

func (t *TopologyTree) parentVia(over map[string]string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		if p, ok := over[id]; ok {
			return p, true
		}
		return t.topo.From(id)
	}
}

func (t *TopologyTree) Children(id string) []string {
	parentOf := t.parentVia(t.Edges())
	var out []string
	for _, n := range t.topo.Nodes() {
		if p, ok := parentOf(n); ok && p == id && n != id {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (t *TopologyTree) DeleteSet(id string) []string {
	kids := topo.ChildIndex(t.topo, t.parentVia(t.Edges()))
	return topo.DescendantClosure(kids, id)
}

func (t *TopologyTree) Normalized() bool {
	for id := range t.Edges() {
		if t.freeOfAncestry(id) {
			continue
		}
		return false
	}
	return true
}

// freeOfAncestry reports that nothing above id could be taken away from it.
func (t *TopologyTree) freeOfAncestry(id string) bool {
	forks := t.forks
	if forks == nil {
		forks = t.topo
	}
	up, ok := forks.From(id)
	if !ok || up == "" {
		return true
	}
	over, ok := forks.From(up)
	return ok && over == ""
}

func (t *TopologyTree) Overridden() []string {
	over := t.Edges()
	out := make([]string, 0, len(over))
	for id := range over {
		if t.freeOfAncestry(id) {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// edgeIntent is collected and diffed once against the form it applies to: a
// structural patch is a difference and cannot be assembled field by field.
type edgeIntent struct {
	set    map[string]json.RawMessage
	remove []string
}

func newEdgeIntent() *edgeIntent {
	return &edgeIntent{set: map[string]json.RawMessage{}}
}

func (t *TopologyTree) Write(set map[string]string, remove []string) error {
	patch := newEdgeIntent()
	for id, parent := range set {
		raw, err := json.Marshal(parent)
		if err != nil {
			return err
		}
		patch.set[id] = raw
	}
	patch.remove = append(patch.remove, remove...)
	return t.apply(patch)
}

// Forget drops every edge naming one of these arias, in either direction: an
// edge pointing at a deleted aria goes too, so the survivor falls back to its history.
func (t *TopologyTree) Forget(ids ...string) error {
	gone := make(map[string]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
	}
	var remove []string
	for id, up := range t.Edges() {
		if gone[id] || gone[up] {
			remove = append(remove, id)
		}
	}
	if len(remove) == 0 {
		return nil
	}
	sort.Strings(remove)
	return t.apply(&edgeIntent{remove: remove})
}

// apply uses the privileged path (presentation is harness state), with Ensure
// because a Forget may name an edge already gone.
func (t *TopologyTree) apply(in *edgeIntent) error {
	base, _ := t.form.Snapshot()
	_, _, err := t.form.applyEffect(form.Build(base, in.set, in.remove), 0, Ensure, true)
	return err
}

var _ topo.Tree = (*TopologyTree)(nil)
