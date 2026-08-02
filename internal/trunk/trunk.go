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
// wrong tree — the same property that lets figwal rebuild its index from
// markers.
type state struct {
	Version int               `json:"version"`
	Parent  map[string]string `json:"parent,omitempty"`
}

// Tree is a presentation hierarchy backed by overrides on disk.
type Tree struct {
	mu    sync.RWMutex
	path  string
	topo  topo.Topology
	over  map[string]string
	dirty bool
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
	for k, v := range s.Parent {
		x.over[k] = v
	}
	return x, nil
}

func (x *Tree) save() error {
	s := state{Version: 1, Parent: map[string]string{}}
	for k, v := range x.over {
		s.Parent[k] = v
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := x.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, x.path); err != nil {
		return err
	}
	x.dirty = false
	return nil
}

// Parent is the override if one exists, else the topology edge.
func (x *Tree) Parent(id string) (string, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.parentLocked(id)
}

func (x *Tree) parentLocked(id string) (string, bool) {
	if p, ok := x.over[id]; ok {
		return p, true
	}
	return x.topo.From(id)
}

func (x *Tree) Children(id string) []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []string
	for _, n := range x.topo.Nodes() {
		if p, ok := x.parentLocked(n); ok && p == id && n != id {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (x *Tree) DeleteSet(id string) []string { return topo.DescendantClosure(x, id) }

// Normalized reports whether every aria still sits where its history says.
// True means a delete's boundary is provably empty and no repair is needed.
func (x *Tree) Normalized() bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.over) == 0
}

// Promote raises id one level: it takes its grandparent's place, and the
// parent it displaced comes to sit under it.
//
// This edits two override edges and returns. It writes nothing to any aria's
// segments, so it is O(1) regardless of history length and cannot fail
// halfway into a corrupt store. The topology is untouched: id still reads
// its history exactly where it did before.
func (x *Tree) Promote(id string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	parent, ok := x.parentLocked(id)
	if !ok || parent == "" {
		return fmt.Errorf("trunk: %q is already a root", id)
	}
	grand, _ := x.parentLocked(parent)
	if grand == id {
		return fmt.Errorf("trunk: %q and %q already swapped", id, parent)
	}
	x.over[id] = grand
	x.over[parent] = id
	x.dirty = true
	return x.save()
}

// Reparent sets an explicit presentation edge. Used by normalization to put
// an aria back where its history says it belongs.
func (x *Tree) Reparent(id, parent string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if up, ok := x.topo.From(id); ok && up == parent {
		delete(x.over, id) // back to the default: no override needed
	} else {
		x.over[id] = parent
	}
	x.dirty = true
	return x.save()
}

// Overridden is every aria whose presentation parent differs from its
// topology parent — exactly the arias that make a delete's boundary
// non-empty, and so the set normalization must make independent.
func (x *Tree) Overridden() []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]string, 0, len(x.over))
	for id := range x.over {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Forget drops an aria's override, for use after it is deleted.
func (x *Tree) Forget(ids ...string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	changed := false
	for _, id := range ids {
		if _, ok := x.over[id]; ok {
			delete(x.over, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return x.save()
}
