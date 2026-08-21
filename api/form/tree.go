// Immutable AVL tree for form snapshots.

package form

import (
	"encoding/json"
	"iter"
	"slices"
	"strings"
)

// ptree is an immutable, string-keyed ordered map of JSON values. The zero
// ptree is a valid empty tree. Every method leaves the receiver untouched.
type ptree struct {
	root *node
}

type node struct {
	key         string
	value       Value
	left, right *node
	height      int
	size        int
}

// treeFromMap builds a tree from a plain map, in O(n log n) for the sort plus
// O(n) for the build: exactly n nodes, no rotations, no rebalancing garbage.
func treeFromMap(m map[string]json.RawMessage) ptree {
	entries := make([]treeEntry, 0, len(m))
	for k, v := range m {
		entries = append(entries, treeEntry{key: k, value: NewValue(v)})
	}
	return treeFromEntries(entries)
}

// treeEntry is a key/value pair for bulk construction.
type treeEntry struct {
	key   string
	value Value
}

// treeFromEntries sorts entries by key and builds a perfectly balanced tree
// bottom-up. Keys must be unique (they always are: they come from a map or a
// JSON object). Bulk-building beats n repeated Sets by a wide margin: no
// comparisons past the sort, no rotations, and no intermediate path copies -
// and it is on the aria-load and WAL-replay paths, so it matters.
func treeFromEntries(entries []treeEntry) ptree {
	slices.SortFunc(entries, func(a, b treeEntry) int { return strings.Compare(a.key, b.key) })
	return ptree{root: buildBalanced(entries)}
}

func buildBalanced(entries []treeEntry) *node {
	if len(entries) == 0 {
		return nil
	}
	mid := len(entries) / 2
	return makeNode(entries[mid].key, entries[mid].value,
		buildBalanced(entries[:mid]), buildBalanced(entries[mid+1:]))
}

// Len returns the number of entries. O(1).
func (t ptree) Len() int { return nodeSize(t.root) }

// Get looks up key. O(log n).
func (t ptree) Get(key string) (Value, bool) {
	for n := t.root; n != nil; {
		switch {
		case key < n.key:
			n = n.left
		case key > n.key:
			n = n.right
		default:
			return n.value, true
		}
	}
	return Value{}, false
}

// Has reports whether key is present.
func (t ptree) Has(key string) bool {
	_, ok := t.Get(key)
	return ok
}

// Set returns a tree containing key bound to value. The receiver is unchanged.
func (t ptree) Set(key string, value Value) ptree {
	root := setNode(t.root, key, value)
	if root == t.root {
		return t
	}
	return ptree{root: root}
}

// Delete returns a tree without key. An absent key returns the receiver.
func (t ptree) Delete(key string) ptree {
	root, found := deleteNode(t.root, key)
	if !found {
		return t
	}
	return ptree{root: root}
}

// Range visits entries in lexical key order, stopping when yield returns false.
func (t ptree) Range(yield func(key string, value Value) bool) {
	rangeNode(t.root, yield)
}

// All iterates entries in lexical key order.
func (t ptree) All() iter.Seq2[string, Value] {
	return func(yield func(string, Value) bool) { rangeNode(t.root, yield) }
}

func setNode(n *node, key string, value Value) *node {
	if n == nil {
		return makeNode(key, value, nil, nil)
	}
	switch {
	case key < n.key:
		left := setNode(n.left, key, value)
		if left == n.left {
			return n
		}
		return balance(makeNode(n.key, n.value, left, n.right))
	case key > n.key:
		right := setNode(n.right, key, value)
		if right == n.right {
			return n
		}
		return balance(makeNode(n.key, n.value, n.left, right))
	default:
		if n.value.Equal(value) {
			return n
		}
		return makeNode(key, value, n.left, n.right)
	}
}

func deleteNode(n *node, key string) (*node, bool) {
	if n == nil {
		return nil, false
	}
	switch {
	case key < n.key:
		left, found := deleteNode(n.left, key)
		if !found {
			return n, false
		}
		return balance(makeNode(n.key, n.value, left, n.right)), true
	case key > n.key:
		right, found := deleteNode(n.right, key)
		if !found {
			return n, false
		}
		return balance(makeNode(n.key, n.value, n.left, right)), true
	}
	if n.left == nil {
		return n.right, true
	}
	if n.right == nil {
		return n.left, true
	}
	successor := minNode(n.right)
	return balance(makeNode(successor.key, successor.value, n.left, deleteMin(n.right))), true
}

func deleteMin(n *node) *node {
	if n.left == nil {
		return n.right
	}
	return balance(makeNode(n.key, n.value, deleteMin(n.left), n.right))
}

func minNode(n *node) *node {
	for n.left != nil {
		n = n.left
	}
	return n
}

func rangeNode(n *node, yield func(string, Value) bool) bool {
	if n == nil {
		return true
	}
	return rangeNode(n.left, yield) && yield(n.key, n.value) && rangeNode(n.right, yield)
}

func makeNode(key string, value Value, left, right *node) *node {
	return &node{
		key: key, value: value, left: left, right: right,
		height: 1 + max(nodeHeight(left), nodeHeight(right)),
		size:   1 + nodeSize(left) + nodeSize(right),
	}
}

func balance(n *node) *node {
	factor := nodeHeight(n.left) - nodeHeight(n.right)
	if factor > 1 {
		if nodeHeight(n.left.left) < nodeHeight(n.left.right) {
			n = makeNode(n.key, n.value, rotateLeft(n.left), n.right)
		}
		return rotateRight(n)
	}
	if factor < -1 {
		if nodeHeight(n.right.right) < nodeHeight(n.right.left) {
			n = makeNode(n.key, n.value, n.left, rotateRight(n.right))
		}
		return rotateLeft(n)
	}
	return n
}

func rotateLeft(n *node) *node {
	r := n.right
	return makeNode(r.key, r.value, makeNode(n.key, n.value, n.left, r.left), r.right)
}

func rotateRight(n *node) *node {
	l := n.left
	return makeNode(l.key, l.value, l.left, makeNode(n.key, n.value, l.right, n.right))
}

func nodeHeight(n *node) int {
	if n == nil {
		return 0
	}
	return n.height
}

func nodeSize(n *node) int {
	if n == nil {
		return 0
	}
	return n.size
}
