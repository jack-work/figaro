package store

// The topology form: the presentation hierarchy as a FORM rather than as a
// hand-rolled file.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/topo"
)

// topologyStump is the reserved name. Outfit stumps are "@" + a hex content
// hash, so a name with a non-hex tail cannot collide with one.
const topologyStump = "@topology"

// stumpFormLog is a FormLog over a stump's form channel. The same channel,
// the same reducer and the same durability as an aria's board; only the node
// it hangs on differs.
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

// TopologyTree is topo.Tree over a form. Every mutator is one patch, so a
// promote's two edges land together or not at all.
type TopologyTree struct {
	form *Form
	topo topo.Topology
}

// OpenTopologyTree mints the reserved stump if it is not there, replays the
// form, and folds a legacy trunks.json in on the way past.
func OpenTopologyTree(s *XwalStore, dir string) (*TopologyTree, error) {
	if err := s.ensureTopologyStump(); err != nil {
		return nil, err
	}
	f, err := OpenForm(&stumpFormLog{store: s, stump: topologyStump})
	if err != nil {
		return nil, err
	}
	t := &TopologyTree{form: f, topo: s.TopologyAdjacency()}
	if err := t.migrate(filepath.Join(dir, "trunks.json")); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *XwalStore) ensureTopologyStump() error { return s.ensureStump(topologyStump) }

// ensureStump mints a reserved stump if it is not there. Reserved stumps are
// how this store gives a form a WELL-KNOWN id: figwal names a stump by a
// string the caller chooses, where every other node gets a system id.
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

// migrate folds a legacy trunks.json into the form, ONCE, and renames it.
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
		if _, _, err := t.form.ApplyEffectPrivileged(message.Patch{Set: set}, 0); err != nil {
			return fmt.Errorf("topology: migrate %s: %w", path, err)
		}
	}
	return os.Rename(path, path+".migrated")
}

func (t *TopologyTree) Close() { t.form.Close() }

// Edges is the override map, decoded from the published snapshot.
func (t *TopologyTree) Edges() map[string]string {
	snap, _ := t.form.Snapshot()
	out := map[string]string{}
	for k := range snap.All() {
		if v := snap.Lookup(k); v != nil && *v != "" {
			out[k] = *v
		}
	}
	return out
}

// Rev is the form's version: it moves on every landed edit, which is exactly
// what a listing's snapshot check wants.
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
	up, ok := t.topo.From(id)
	if !ok || up == "" {
		return true
	}
	over, ok := t.topo.From(up)
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

// Promote raises id one level, as ONE patch. Two keys in one record is what
// makes it atomic; the file it replaces rewrote the whole document and could
// land half of it.
func (t *TopologyTree) Promote(id string) error {
	parent, ok := t.Parent(id)
	if !ok || parent == "" {
		return fmt.Errorf("trunk: %q is already a root", id)
	}
	grand, _ := t.Parent(parent)
	if grand == id {
		return fmt.Errorf("trunk: %q and %q already swapped", id, parent)
	}
	idUp, _ := t.topo.From(id)
	parentUp, _ := t.topo.From(parent)

	patch := message.Patch{Set: map[string]json.RawMessage{}}
	edge(&patch, id, grand, idUp)
	edge(&patch, parent, id, parentUp)
	return t.apply(patch)
}

// edge records a presentation edge, REMOVING the override when it agrees
// with the topology. One rule, so an aria promoted back to where its history
// puts it leaves no trace; otherwise Normalized() stays false forever and
// every later delete repairs an empty boundary.
func edge(p *message.Patch, id, parent, topoParent string) {
	if parent == topoParent {
		p.Remove = append(p.Remove, id)
		return
	}
	raw, _ := json.Marshal(parent)
	p.Set[id] = raw
}

func (t *TopologyTree) Reparent(id, parent string) error {
	up, _ := t.topo.From(id)
	patch := message.Patch{Set: map[string]json.RawMessage{}}
	edge(&patch, id, parent, up)
	return t.apply(patch)
}

// Forget drops every edge that names one of these arias, in either
// direction: for use once they are deleted. An edge POINTING at a deleted
// aria goes too, so the survivor falls back to its history rather than
// hanging off a parent that is no longer there.
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
	return t.apply(message.Patch{Remove: remove})
}

// apply writes through the form's privileged path: presentation is harness
// state, and Ensure because a Forget may name an edge already gone.
func (t *TopologyTree) apply(p message.Patch) error {
	_, _, err := t.form.applyEffect(p, 0, Ensure, true)
	return err
}

var _ topo.Tree = (*TopologyTree)(nil)
