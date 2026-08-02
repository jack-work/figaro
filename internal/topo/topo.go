// Package topo is the aria hierarchy figaro renders and deletes by.
//
// There are two hierarchies over the same arias and they answer different
// questions:
//
//   - TOPOLOGY, on disk as .from plus a per-channel .fork base: where my
//     history comes from. Fixed at fork time; changing it changes what an
//     aria can read. This is correctness, and figwal owns it.
//   - PRESENTATION, this package: what an aria appears under, and what a
//     delete takes with it. This is intent, and nothing on disk can
//     reconstruct it.
//
// Without the trunk capability the two are the same tree and Tree reads
// straight through to the topology. With it, internal/trunk supplies a
// pstate-backed tree that may differ — a promoted aria appears above the
// ancestor it still inherits from.
//
// The rule that keeps this safe: FORKING NEVER CONSULTS A Tree. Owner and
// ForkAt climb .from. A presentation edge must never decide where data
// comes from.
package topo

import (
	"errors"
	"sort"
)

// ErrNoPromote reports that the running configuration has no presentation
// hierarchy to edit, so promotion is meaningless rather than merely failed.
var ErrNoPromote = errors.New("topo: promote needs the trunk capability")

// Tree is the presentation hierarchy.
type Tree interface {
	// Parent is the aria this one appears under; "" for a root.
	Parent(id string) (string, bool)
	// Children is every aria appearing directly under id.
	Children(id string) []string
	// DeleteSet is id plus its descendants: what a delete of id takes.
	DeleteSet(id string) []string
	// Promote raises id one level. ErrNoPromote without the capability.
	Promote(id string) error
	// Normalized reports whether this tree agrees with the topology, which
	// is what makes a delete's boundary provably empty.
	Normalized() bool
	// Overridden is every aria whose presentation parent differs from its
	// topology parent. Empty exactly when Normalized.
	Overridden() []string
}

// Topology is the adjacency figwal owns, as this package needs it.
type Topology interface {
	From(id string) (string, bool)
	ChildrenOf(id string) []string
	Nodes() []string
}

// FromTopology is the presentation tree of a figaro with no trunk
// capability: the topology IS the hierarchy. It is always normalized, so a
// delete never has a boundary to repair.
func FromTopology(t Topology) Tree { return topoTree{t} }

type topoTree struct{ t Topology }

func (x topoTree) Parent(id string) (string, bool) { return x.t.From(id) }
func (x topoTree) Children(id string) []string     { return x.t.ChildrenOf(id) }
func (x topoTree) Promote(string) error            { return ErrNoPromote }
func (x topoTree) Normalized() bool                { return true }
func (x topoTree) Overridden() []string            { return nil }

func (x topoTree) DeleteSet(id string) []string {
	return DescendantClosure(ChildIndex(x.t, x.t.From), id)
}

// DescendantClosure is id plus everything under it, breadth-first.
//
// Takes the whole parent->children adjacency in ONE pass. Asking a Tree for
// Children per node makes this O(n^2), because both implementations answer
// that by scanning every node.
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

// ChildIndex is the presentation adjacency, built once.
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
// set whose TOPOLOGY ancestry runs through it. They must absorb the prefix
// they borrow before any directory in the set is unlinked.
//
// Empty whenever the presentation tree is normalized, which is why a
// trunkless figaro never repairs anything.
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
