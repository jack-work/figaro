package form

import (
	"encoding/json"
	"iter"
)

type Node struct {
	n *node
}

type Stamp struct {
	value  *canonBox
	kids   *node
	branch bool
}

func (s Snapshot) RootNode() Node {
	return Node{n: &node{kids: s.t, branch: true}}
}

func wrapNode(n *node) Node {
	if !n.branch {
		if members, isObj := n.value.members(); isObj && len(members) > 0 {
			return Node{n: leafOrBranch(n.key, n.value)}
		}
	}
	return Node{n: n}
}

func (n Node) Valid() bool { return n.n != nil }

func (n Node) Key() string { return n.n.key }

func (n Node) Branch() bool { return n.n.branch && n.n.kids.root != nil }

func (n Node) Leaf() bool { return !n.n.branch }

func (n Node) Len() int {
	if !n.n.branch {
		return 0
	}
	return nodeSize(n.n.kids.root)
}

func (n Node) Value() Value {
	if n.n.branch {
		return entryValue(n.n)
	}
	return n.n.value
}

func (n Node) Encode() (json.RawMessage, error) {
	if n.n.branch {
		return encodeTree(n.n.kids)
	}
	return n.n.value.Raw(), nil
}

func (n Node) Stamp() Stamp {
	if n.n.branch {
		return Stamp{kids: n.n.kids.root, branch: true}
	}
	return Stamp{value: n.n.value.c}
}

func (n Node) Children() iter.Seq[Node] {
	return func(yield func(Node) bool) {
		if !n.n.branch {
			return
		}
		rangeEntries(n.n.kids.root, func(c *node) bool { return yield(wrapNode(c)) })
	}
}
