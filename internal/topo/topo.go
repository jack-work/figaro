// Package topo is the aria hierarchy figaro renders and deletes by.
package topo

import (
	"errors"
	"maps"
	"sort"
)

// ErrNoPromote: the running configuration has no presentation hierarchy.
var ErrNoPromote = errors.New("topo: promote needs the trunk capability")

type Tree interface {
	Parent(id string) (string, bool)
	Children(id string) []string
	// DeleteSet is id plus its descendants.
	DeleteSet(id string) []string
	Promote(id string) error
	Demote(id, under string) error
	// Normalized reports whether this tree agrees with the topology.
	Normalized() bool
	// Overridden is empty exactly when Normalized.
	Overridden() []string
	// Edges is the raw override map; nil when presentation is the topology.
	Edges() map[string]string
	// Rev bumps whenever Edges changes.
	Rev() uint64
	// Forget drops every edge naming any of these arias, in either direction.
	Forget(ids ...string) error
	// Reparent pins an aria under a parent.
	Reparent(id, parent string) error
}

type Topology interface {
	From(id string) (string, bool)
	ChildrenOf(id string) []string
	Nodes() []string
}

// FromTopology is the presentation tree without a trunk capability: always
// normalized.
func FromTopology(t Topology) Tree { return topoTree{t} }

type topoTree struct{ t Topology }

func (x topoTree) Parent(id string) (string, bool) { return x.t.From(id) }
func (x topoTree) Children(id string) []string     { return x.t.ChildrenOf(id) }
func (x topoTree) Promote(string) error            { return ErrNoPromote }
func (x topoTree) Demote(string, string) error     { return ErrNoPromote }
func (x topoTree) Normalized() bool                { return true }
func (x topoTree) Overridden() []string            { return nil }
func (x topoTree) Edges() map[string]string        { return nil }
func (x topoTree) Rev() uint64                     { return 0 }
func (x topoTree) Forget(...string) error          { return nil }
func (x topoTree) Reparent(string, string) error   { return nil }

func (x topoTree) DeleteSet(id string) []string {
	return DescendantClosure(ChildIndex(x.t, x.t.From), id)
}

// Present is the topology with every sound override applied.
func Present(parent map[string]string, edges map[string]string) map[string]string {
	out := make(map[string]string, len(parent))
	maps.Copy(out, parent)
	moved := map[string]bool{}
	for id, up := range edges {
		if _, known := out[id]; !known {
			continue
		}
		if _, known := out[up]; !known && up != "" {
			continue
		}
		out[id] = up
		moved[id] = true
	}
	// A promote writes two edges at once, so edges are judged together.
	for {
		id := cycleCulprit(out, moved)
		if id == "" {
			return out
		}
		out[id] = parent[id]
		delete(moved, id)
	}
}

// cycleCulprit is the lowest moved aria on a cycle, or ""; lowest so the
// repair is the same on every machine.
func cycleCulprit(parent map[string]string, moved map[string]bool) string {
	ids := make([]string, 0, len(parent))
	for id := range parent {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, start := range ids {
		seen := map[string]bool{}
		for cur := start; cur != ""; cur = parent[cur] {
			if seen[cur] {
				return lowestMoved(parent, cur, moved)
			}
			seen[cur] = true
		}
	}
	return ""
}

func lowestMoved(parent map[string]string, on string, moved map[string]bool) string {
	best := ""
	seen := map[string]bool{}
	for cur := on; !seen[cur]; cur = parent[cur] {
		seen[cur] = true
		if moved[cur] && (best == "" || cur < best) {
			best = cur
		}
	}
	return best
}

func DescendantClosure(kids map[string][]string, id string) []string {
	seen := map[string]bool{id: true}
	out := []string{id}
	for i := 0; i < len(out); i++ {
		for _, c := range kids[out[i]] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func ChildIndex(t Topology, parentOf func(string) (string, bool)) map[string][]string {
	nodes := t.Nodes()
	out := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		if p, ok := parentOf(n); ok && p != n {
			out[p] = append(out[p], n)
		}
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// Boundary is the survivors a delete would orphan: arias outside the delete
// set whose topology ancestry runs through it. They must absorb the prefix
// they borrow before any directory in the set is unlinked.
func Boundary(t Topology, deleteSet []string) []string {
	doomed := make(map[string]bool, len(deleteSet))
	for _, id := range deleteSet {
		doomed[id] = true
	}
	var out []string
	for _, id := range t.Nodes() {
		if doomed[id] {
			continue
		}
		for up, ok := t.From(id); ok && up != ""; up, ok = t.From(up) {
			if doomed[up] {
				out = append(out, id)
				break
			}
		}
	}
	return out
}
