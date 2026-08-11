// Pointer-identity diff over the persistent form tree.
//
// The thesis: Set and Delete path-copy, so every subtree that an edit did not
// touch is *the same pointer* in both trees. `prev == next` therefore proves a
// whole subtree is unchanged and can be skipped without looking inside it. A
// patch touching k keys leaves only O(k log n) nodes un-shared, so the diff
// costs O(k log n) instead of O(n).
//
// The correctness hazard: AVL rebalancing moves nodes, so the two trees need
// not have the same shape and a naive "walk both structures in lockstep" is
// wrong. This walks by KEY, not by shape: at each step the pivot is next's key
// and prev is split around it, which is exactly a merge-join over two sorted
// key sequences. The pointer check is a pure short-circuit layered on top -
// removing every `prev == next` line would leave a correct (slower) diff.
//
// Cost: the common case (same root key all the way down, i.e. a path copy with
// no rotation at the root) splits at the root's exact match, which returns
// prev's ORIGINAL child pointers, so the short-circuit keeps firing all the way
// down. Where rotations did move things, splitNode rebuilds a small spine of
// scratch nodes and the short-circuit stops helping on that path; the diff
// stays correct and degrades to O(n log n) for two wholly unrelated trees.
// Scratch nodes never escape into a stored tree: nothing here mutates.

package form

import (
	"encoding/json"
	"slices"
)

// diffTrees computes the patch that transforms prev into next: keys added or
// changed land in Set (bound to next's raw bytes), keys present in prev and
// absent from next land in Remove, sorted. Values compare with Value.Equal, so
// a key whose object was merely reordered is not reported as a change.
func diffTrees(prev, next *node) Patch {
	var d differ
	d.walk(prev, next)
	slices.Sort(d.patch.Remove)
	return d.patch
}

// differ accumulates the patch and counts work done. The counters exist so the
// tests can *prove* the short-circuit rather than assert it in prose; they cost
// an increment on paths that already allocate.
type differ struct {
	patch   Patch
	visited int // node pairs actually compared
	shared  int // subtrees skipped by pointer identity
	splits  int // splitNode calls (the only allocating step)
}

func (d *differ) set(key string, v Value) {
	if d.patch.Set == nil {
		d.patch.Set = make(map[string]json.RawMessage)
	}
	d.patch.Set[key] = v.Raw()
}

func (d *differ) walk(prev, next *node) {
	if prev == next { // includes both nil
		if prev != nil {
			d.shared++
		}
		return
	}
	if prev == nil {
		d.setAll(next)
		return
	}
	if next == nil {
		d.removeAll(prev)
		return
	}
	d.visited++

	left, value, found, right := d.split(prev, next.key)
	if !found || !value.Equal(next.value) {
		d.set(next.key, next.value)
	}
	d.walk(left, next.left)
	d.walk(right, next.right)
}

// split partitions n by key into (keys <, the binding at key, keys >).
//
// An exact hit at n returns n's original child pointers untouched, which is
// what keeps the pointer short-circuit alive through an un-rotated path copy.
// Otherwise the spine is rebuilt with makeNode; the result is a valid BST with
// correct size/height metadata but is NOT guaranteed AVL-balanced. That is
// fine: these nodes are read by this diff and then dropped.
func (d *differ) split(n *node, key string) (left *node, value Value, found bool, right *node) {
	if n == nil {
		return nil, Value{}, false, nil
	}
	d.splits++
	switch {
	case key < n.key:
		left, value, found, right = d.split(n.left, key)
		return left, value, found, makeNode(n.key, n.value, right, n.right)
	case key > n.key:
		left, value, found, right = d.split(n.right, key)
		return makeNode(n.key, n.value, n.left, left), value, found, right
	default:
		return n.left, n.value, true, n.right
	}
}

func (d *differ) setAll(n *node) {
	if n == nil {
		return
	}
	d.setAll(n.left)
	d.set(n.key, n.value)
	d.setAll(n.right)
}

func (d *differ) removeAll(n *node) {
	if n == nil {
		return
	}
	d.removeAll(n.left)
	d.patch.Remove = append(d.patch.Remove, n.key)
	d.removeAll(n.right)
}
