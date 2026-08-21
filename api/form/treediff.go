// Pointer-identity diff over the persistent form tree.

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
