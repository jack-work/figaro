package form

import (
	"iter"
	"sort"
)

// ptree is an immutable AVL map; the zero ptree is empty.
type ptree struct {
	root *node
}

// node is one path segment holding a leaf or a subtree, never both; a key
// containing a dot is a single segment at its level.
type node struct {
	key    string
	value  Value
	kids   ptree
	branch bool

	left, right *node
	height      int
	size        int
}

func (t ptree) Len() int { return nodeSize(t.root) }

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

func (t ptree) Has(key string) bool {
	_, ok := t.Get(key)
	return ok
}

func (t ptree) Set(key string, value Value) ptree {
	root := setNode(t.root, key, value)
	if root == t.root {
		return t
	}
	return ptree{root: root}
}

func (t ptree) Delete(key string) ptree {
	root, found := deleteNode(t.root, key)
	if !found {
		return t
	}
	return ptree{root: root}
}

func (t ptree) Range(yield func(key string, value Value) bool) {
	rangeNode(t.root, yield)
}

func (t ptree) All() iter.Seq2[string, Value] {
	return func(yield func(string, Value) bool) { rangeNode(t.root, yield) }
}

func setEntry(n *node, e *node) *node {
	if n == nil {
		return &node{key: e.key, value: e.value, kids: e.kids, branch: e.branch, height: 1, size: 1}
	}
	switch {
	case e.key < n.key:
		return balance(cloneWith(n, setEntry(n.left, e), n.right))
	case e.key > n.key:
		return balance(cloneWith(n, n.left, setEntry(n.right, e)))
	}
	out := *n
	out.value, out.kids, out.branch = e.value, e.kids, e.branch
	return &out
}

func cloneWith(n *node, left, right *node) *node {
	out := *n
	out.left, out.right = left, right
	out.height = 1 + max(nodeHeight(left), nodeHeight(right))
	out.size = 1 + nodeSize(left) + nodeSize(right)
	return &out
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
		return balance(cloneWith(n, left, n.right))
	case key > n.key:
		right := setNode(n.right, key, value)
		if right == n.right {
			return n
		}
		return balance(cloneWith(n, n.left, right))
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
		return balance(cloneWith(n, left, n.right)), true
	case key > n.key:
		right, found := deleteNode(n.right, key)
		if !found {
			return n, false
		}
		return balance(cloneWith(n, n.left, right)), true
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
	return balance(cloneWith(n, deleteMin(n.left), n.right))
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
			n = cloneWith(n, rotateLeft(n.left), n.right)
		}
		return rotateRight(n)
	}
	if factor < -1 {
		if nodeHeight(n.right.right) < nodeHeight(n.right.left) {
			n = cloneWith(n, n.left, rotateRight(n.right))
		}
		return rotateLeft(n)
	}
	return n
}

func rotateLeft(n *node) *node {
	r := n.right
	return cloneWith(r, cloneWith(n, n.left, r.left), r.right)
}

func rotateRight(n *node) *node {
	l := n.left
	return cloneWith(l, l.left, cloneWith(n, l.right, n.right))
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

// entry resolves longest segment first, so a key containing a dot resolves
// to itself.
func (t ptree) entry(path string) (*node, bool) {
	cur, rest := t, path
	for {
		n := lookupLongest(cur.root, rest)
		if n == nil {
			return nil, false
		}
		if len(n.key) == len(rest) {
			return n, true
		}
		if !n.branch {
			return nil, false
		}
		cur, rest = n.kids, rest[len(n.key)+1:]
	}
}

func lookupLongest(n *node, rest string) *node {
	var best *node
	for n != nil {
		switch {
		case rest < n.key:
			n = n.left
		case rest > n.key:
			if len(n.key) < len(rest) && rest[len(n.key)] == '.' && rest[:len(n.key)] == n.key {
				if best == nil || len(n.key) > len(best.key) {
					best = n
				}
			}
			n = n.right
		default:
			return n
		}
	}
	return best
}

func (t ptree) setPath(segs []string, v Value) ptree {
	if len(segs) == 0 {
		return t
	}
	head := segs[0]
	if len(segs) == 1 {
		return ptree{root: setEntry(t.root, leafOrBranch(head, v))}
	}
	var kids ptree
	if cur := lookupExact(t.root, head); cur != nil && cur.branch {
		kids = cur.kids
	}
	kids = kids.setPath(segs[1:], v)
	return ptree{root: setEntry(t.root, &node{key: head, kids: kids, branch: true})}
}

func (t ptree) deletePath(segs []string) (ptree, bool) {
	if len(segs) == 0 {
		return t, false
	}
	head := segs[0]
	if len(segs) == 1 {
		root, found := deleteNode(t.root, head)
		return ptree{root: root}, found
	}
	if n := lookupExact(t.root, joinSegs(segs)); n != nil {
		root, found := deleteNode(t.root, joinSegs(segs))
		return ptree{root: root}, found
	}
	cur := lookupExact(t.root, head)
	if cur == nil || !cur.branch {
		return t, false
	}
	kids, found := cur.kids.deletePath(segs[1:])
	if !found {
		return t, false
	}
	if kids.root == nil {
		root, _ := deleteNode(t.root, head)
		return ptree{root: root}, true
	}
	return ptree{root: setEntry(t.root, &node{key: head, kids: kids, branch: true})}, true
}

func lookupExact(n *node, key string) *node {
	for n != nil {
		switch {
		case key < n.key:
			n = n.left
		case key > n.key:
			n = n.right
		default:
			return n
		}
	}
	return nil
}

func joinSegs(segs []string) string {
	out := segs[0]
	for _, s := range segs[1:] {
		out += "." + s
	}
	return out
}

func (t ptree) leaves(prefix string, yield func(string, Value) bool) bool {
	return rangeEntries(t.root, func(n *node) bool {
		path := n.key
		if prefix != "" {
			path = prefix + "." + n.key
		}
		if !n.branch {
			return yield(path, n.value)
		}
		return n.kids.leaves(path, yield)
	})
}

func rangeEntries(n *node, yield func(*node) bool) bool {
	if n == nil {
		return true
	}
	if !rangeEntries(n.left, yield) {
		return false
	}
	if !yield(n) {
		return false
	}
	return rangeEntries(n.right, yield)
}

// leafOrBranch makes every object a branch, so the storage shape and the
// read shape agree.
func leafOrBranch(key string, v Value) *node {
	members, isObj := v.members()
	if !isObj {
		return &node{key: key, value: v}
	}
	var kids ptree
	names := make([]string, 0, len(members))
	for k := range members {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		kids = ptree{root: setEntry(kids.root, leafOrBranch(k, members[k]))}
	}
	return &node{key: key, kids: kids, branch: true}
}
