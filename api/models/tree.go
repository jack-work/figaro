package models

import "strings"

const (
	LeavesKey   = "models"
	BranchesKey = "upstreams"
	RouteSep    = "/"
	MaxDepth    = 6
)

type Node struct {
	Leaves   map[string]Entry `json:"models,omitempty"`
	Branches map[string]Node  `json:"upstreams,omitempty"`
}

func Path(model string) []string {
	segs := strings.Split(model, RouteSep)
	if len(segs) > MaxDepth {
		return []string{model}
	}
	for _, s := range segs {
		if s == "" {
			return []string{model}
		}
	}
	return segs
}

func Route(provider, model string) []string {
	if provider == "" {
		return nil
	}
	if model == "" {
		return []string{provider}
	}
	return append([]string{provider}, Path(model)...)
}

func EntryKey(provider, model string) string {
	segs := Path(model)
	var b strings.Builder
	b.WriteString(provider)
	for _, s := range segs[:len(segs)-1] {
		b.WriteString("." + BranchesKey + "." + s)
	}
	b.WriteString("." + LeavesKey + "." + segs[len(segs)-1])
	return b.String()
}

func NewNode(flat map[string]Entry) Node {
	var n Node
	for id, e := range flat {
		n.Place(id, e)
	}
	return n
}

func (n *Node) Place(id string, e Entry) {
	n.place(Path(id), e)
}

func (n *Node) place(segs []string, e Entry) {
	if len(segs) == 1 {
		if n.Leaves == nil {
			n.Leaves = map[string]Entry{}
		}
		n.Leaves[segs[0]] = e
		return
	}
	if n.Branches == nil {
		n.Branches = map[string]Node{}
	}
	b := n.Branches[segs[0]]
	b.place(segs[1:], e)
	n.Branches[segs[0]] = b
}

func (n Node) Find(id string) (Entry, bool) {
	segs := Path(id)
	for _, s := range segs[:len(segs)-1] {
		b, ok := n.Branches[s]
		if !ok {
			return Entry{}, false
		}
		n = b
	}
	e, ok := n.Leaves[segs[len(segs)-1]]
	return e, ok
}

func (n Node) Flat() map[string]Entry {
	out := map[string]Entry{}
	n.flatten("", out)
	return out
}

func (n Node) flatten(prefix string, out map[string]Entry) {
	for id, e := range n.Leaves {
		out[prefix+id] = e
	}
	for name, b := range n.Branches {
		b.flatten(prefix+name+RouteSep, out)
	}
}

func (n Node) Empty() bool { return len(n.Leaves) == 0 && len(n.Branches) == 0 }
