// Package trunk is the OPTIONAL presentation hierarchy: a durable tree of
// aria-under-aria that a user edits with promote, independent of where each
// aria's history actually comes from.
//
// Nothing outside this package imports it. A figaro built without the trunk
// capability never constructs it, and internal/topo.FromTopology answers
// every question from .from instead. That is the whole dependency: this
// package adds Promote and changes what a delete takes, and nothing else.
//
// It owns NO BYTES of aria history. Every edge here is presentation. The
// topology on disk is untouched by everything in this file, which is why a
// promote is instant and cannot corrupt a store.
package trunk

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/jack-work/figaro/internal/topo"
)

// state is the durable form: an OVERRIDE per aria, never a full tree.
//
// Absent an override, presentation falls through to the topology. So a lost
// or truncated pstate degrades to the truthful default rather than to a
// wrong tree: the same property that lets figwal rebuild its index from
// markers.
type state struct {
	Version int               `json:"version"`
	Parent  map[string]string `json:"parent,omitempty"`
}

// stateVersion is the on-disk format. Open REFUSES anything newer rather
// than silently reading a file it does not understand: a version nobody
// validates is decoration.
const stateVersion = 1

// Tree is a presentation hierarchy backed by overrides on disk.
type Tree struct {
	mu   sync.RWMutex
	path string
	topo topo.Topology
	over map[string]string
	rev  uint64
}

// Open loads the overrides beside a store, or starts empty.
func Open(dir string, t topo.Topology) (*Tree, error) {
	x := &Tree{path: filepath.Join(dir, "trunks.json"), topo: t, over: map[string]string{}}
	b, err := os.ReadFile(x.path)
	if os.IsNotExist(err) {
		return x, nil
	}
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("trunk: parse %s: %w", x.path, err)
	}
	if s.Version > stateVersion {
		return nil, fmt.Errorf("trunk: %s is version %d, this figaro understands %d",
			x.path, s.Version, stateVersion)
	}
	x.over = maps.Clone(s.Parent)
	if x.over == nil {
		x.over = map[string]string{}
	}
	return x, nil
}

func (x *Tree) save() error {
	x.rev++
	s := state{Version: stateVersion, Parent: maps.Clone(x.over)}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// fsync the file BEFORE the rename and the directory AFTER it: the
	// content must be durable before anything points at it, and the rename
	// must be durable before we claim it happened. Without both a promote
	// can simply vanish -- this file is its only record, since nothing on
	// disk can reconstruct presentation intent.
	tmp := x.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, x.path); err != nil {
		os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(x.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ONE LOCKING RULE, and it is the only thing standing between this file
// and a deadlock: x.mu guards the override map and NOTHING ELSE. No method
// calls x.topo while holding it. The topology answers out of the store's
// snapshot, and rebuilding that snapshot reads Edges() -- so a topology
// call under the write lock waits on a reader that waits on us.

// Parent is the override if one exists, else the topology edge.
func (x *Tree) Parent(id string) (string, bool) {
	x.mu.RLock()
	p, ok := x.over[id]
	x.mu.RUnlock()
	if ok {
		return p, true
	}
	return x.topo.From(id)
}

// Edges is the override map. The caller applies it over a topology it
// already holds, which is what a listing does once per snapshot.
func (x *Tree) Edges() map[string]string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return maps.Clone(x.over)
}

// Rev counts edits, so a cache keyed on the topology alone still notices a
// promote.
func (x *Tree) Rev() uint64 {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.rev
}

// parentVia answers from a snapshot of the overrides, for bulk walks.
func (x *Tree) parentVia(over map[string]string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		if p, ok := over[id]; ok {
			return p, true
		}
		return x.topo.From(id)
	}
}

func (x *Tree) Children(id string) []string {
	parentOf := x.parentVia(x.Edges())
	var out []string
	for _, n := range x.topo.Nodes() {
		if p, ok := parentOf(n); ok && p == id && n != id {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (x *Tree) DeleteSet(id string) []string {
	kids := topo.ChildIndex(x.topo, x.parentVia(x.Edges()))
	return topo.DescendantClosure(kids, id)
}

// Normalized reports whether any aria still sits away from where its
// history lives. Delete takes this path, so it answers on the first
// survivor rather than allocating and sorting the whole list.
func (x *Tree) Normalized() bool {
	for id := range x.Edges() {
		if x.freeOfAncestry(id) {
			continue
		}
		return false
	}
	return true
}

// freeOfAncestry reports that nothing above id could be taken away from it:
// it owns its history outright, or the only thing above it is the root.
func (x *Tree) freeOfAncestry(id string) bool {
	up, ok := x.topo.From(id)
	if !ok || up == "" {
		return true
	}
	over, ok := x.topo.From(up)
	return ok && over == ""
}

// Promote raises id one level: it takes its grandparent's place, and the
// parent it displaced comes to sit under it.
//
// This edits two override edges and returns. It writes nothing to any aria's
// segments, so it is O(1) regardless of history length and cannot fail
// halfway into a corrupt store. The topology is untouched: id still reads
// its history exactly where it did before.
func (x *Tree) Promote(id string) error {
	parent, ok := x.Parent(id)
	if !ok || parent == "" {
		return fmt.Errorf("trunk: %q is already a root", id)
	}
	grand, _ := x.Parent(parent)
	if grand == id {
		return fmt.Errorf("trunk: %q and %q already swapped", id, parent)
	}
	idUp, _ := x.topo.From(id)
	parentUp, _ := x.topo.From(parent)

	x.mu.Lock()
	defer x.mu.Unlock()
	x.setLocked(id, grand, idUp)
	x.setLocked(parent, id, parentUp)
	return x.save()
}

// setLocked records a presentation edge, dropping the override entirely
// when it agrees with the topology. ONE rule, so an aria promoted back to
// where its history puts it leaves no trace -- otherwise Normalized() stays
// false forever and every later delete repairs an empty boundary.
func (x *Tree) setLocked(id, parent, topoParent string) {
	if parent == topoParent {
		delete(x.over, id)
		return
	}
	x.over[id] = parent
}

// Reparent sets an explicit presentation edge. Used by normalization to put
// an aria back where its history says it belongs.
func (x *Tree) Reparent(id, parent string) error {
	up, _ := x.topo.From(id)
	x.mu.Lock()
	defer x.mu.Unlock()
	x.setLocked(id, parent, up)
	return x.save()
}

// Overridden is every aria whose presentation parent differs from its
// topology parent AND which still reads its history through an ancestor -
// exactly the arias that can make a delete's boundary non-empty.
//
// The second condition is what makes normalization terminate. Absorbing an
// aria's history empties its .from, after which no delete can orphan it and
// its presentation edge is free; keeping it listed here would re-absorb it
// on every run and leave Normalized() false forever, so the operation meant
// to establish the invariant would never report it established.
func (x *Tree) Overridden() []string {
	over := x.Edges()
	out := make([]string, 0, len(over))
	for id := range over {
		if x.freeOfAncestry(id) {
			continue // nothing above it that a delete could take away
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Forget drops every edge that names one of these arias, in either
// direction: for use once they are deleted. An edge POINTING at a deleted
// aria goes too, so the survivor falls back to its history rather than
// hanging off a parent that is no longer there.
func (x *Tree) Forget(ids ...string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	gone := make(map[string]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
	}
	changed := false
	for id, up := range x.over {
		if gone[id] || gone[up] {
			delete(x.over, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return x.save()
}
