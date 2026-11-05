package fuzzy

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

const ValueCap = 160

type Entry struct {
	Path     string
	key      Text
	value    string
	ValueCut bool
}

type child struct {
	key string
	n   *node
}

func (e *Entry) ValueIndex(lowerTerm string) int { return strings.Index(e.value, lowerTerm) }

type node struct {
	stamp form.Stamp
	kids  []child
	entry *Entry
}

func (n *node) child(key string) *node {
	if n == nil {
		return nil
	}
	i := sort.Search(len(n.kids), func(i int) bool { return n.kids[i].key >= key })
	if i < len(n.kids) && n.kids[i].key == key {
		return n.kids[i].n
	}
	return nil
}

func (n *node) collect(out []*Entry) []*Entry {
	if n.entry != nil {
		return append(out, n.entry)
	}
	for _, c := range n.kids {
		out = c.n.collect(out)
	}
	return out
}

type Index struct {
	root    *node
	flat    []*Entry
	Reused  int
	Rebuilt int
}

func (ix *Index) Update(snap form.Snapshot) {
	ix.Reused, ix.Rebuilt = 0, 0
	root := ix.build(snap.RootNode(), ix.root, "")
	if root != ix.root || ix.flat == nil {
		ix.flat = root.collect(ix.flat[:0])
	}
	ix.root = root
}

func (ix *Index) Entries() []*Entry { return ix.flat }

func (ix *Index) build(n form.Node, old *node, path string) *node {
	st := n.Stamp()
	if old != nil && old.stamp == st {
		ix.Reused++
		return old
	}
	out := &node{stamp: st}
	if n.Leaf() {
		ix.Rebuilt++
		if path != "" {
			out.entry = newEntry(path, n.Value().Raw())
		}
		return out
	}
	for c := range n.Children() {
		k := c.Key()
		sub := path + "." + k
		if path == "" {
			sub = k
		}
		out.kids = append(out.kids, child{key: k, n: ix.build(c, old.child(k), sub)})
	}
	sort.Slice(out.kids, func(a, b int) bool { return out.kids[a].key < out.kids[b].key })
	return out
}

func newEntry(path string, raw json.RawMessage) *Entry {
	text := valueText(raw)
	e := &Entry{Path: path, key: NewText(path)}
	if len(text) > ValueCap {
		text, e.ValueCut = text[:ValueCap], true
	}
	e.value = strings.Clone(strings.ToLower(text))
	return e
}

func valueText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		return buf.String()
	}
	return string(raw)
}

type Hit struct {
	Entry   *Entry
	Score   int
	OnKey   bool
	ValueAt int
}

func (h Hit) KeyPositions(m *Matcher, query string) []int {
	var out []int
	for _, t := range strings.Fields(query) {
		out = append(out, m.Positions(&h.Entry.key, []rune(t))...)
	}
	sort.Ints(out)
	return out
}

func (ix *Index) Query(m *Matcher, query string) []Hit {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return nil
	}
	patterns := make([][]rune, len(terms))
	lower := make([]string, len(terms))
	for i, t := range terms {
		patterns[i] = []rune(t)
		lower[i] = strings.ToLower(t)
	}
	var hits []Hit
	for _, e := range ix.Entries() {
		h := Hit{Entry: e, ValueAt: -1}
		ok := true
		for i := range terms {
			if km, found := m.Match(&e.key, patterns[i]); found {
				h.OnKey = true
				h.Score += km.Score
				continue
			}
			if at := strings.Index(e.value, lower[i]); at >= 0 {
				if h.ValueAt < 0 {
					h.ValueAt = at
				}
				continue
			}
			ok = false
			break
		}
		if ok {
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].OnKey != hits[b].OnKey {
			return hits[a].OnKey
		}
		return hits[a].Score > hits[b].Score
	})
	return hits
}
