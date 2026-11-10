package fuzzy

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/form"
	"github.com/junegunn/fzf/src/util"
)

const ValueCap = 160

type Entry struct {
	Path     string
	Keys     []string
	key      Text
	value    []byte
	wide     *Text
	ValueCut bool
}

type child struct {
	key string
	n   *node
}

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
	root      *node
	flat      []*Entry
	Reused    int
	Rebuilt   int
	lastTerms []Term
	lastPool  []*Entry
}

func (ix *Index) Update(snap form.Snapshot) {
	ix.Reused, ix.Rebuilt = 0, 0
	root := ix.build(snap.RootNode(), ix.root, "", nil)
	if root != ix.root || ix.flat == nil {
		ix.flat = root.collect(ix.flat[:0])
		ix.lastTerms, ix.lastPool = nil, nil
	}
	ix.root = root
}

func (ix *Index) Entries() []*Entry { return ix.flat }

func (ix *Index) build(n form.Node, old *node, path string, keys []string) *node {
	st := n.Stamp()
	if old != nil && old.stamp == st {
		ix.Reused++
		return old
	}
	out := &node{stamp: st}
	if n.Leaf() {
		ix.Rebuilt++
		if path != "" {
			out.entry = newEntry(path, keys, n.Value().Raw())
		}
		return out
	}
	for c := range n.Children() {
		k := c.Key()
		sub := path + "." + k
		if path == "" {
			sub = k
		}
		out.kids = append(out.kids, child{key: k, n: ix.build(c, old.child(k), sub, append(keys[:len(keys):len(keys)], k))})
	}
	sort.Slice(out.kids, func(a, b int) bool { return out.kids[a].key < out.kids[b].key })
	return out
}

func (e *Entry) valueText() Text {
	if e.wide != nil {
		return *e.wide
	}
	return Text{chars: util.ToChars(e.value)}
}

func newEntry(path string, keys []string, raw json.RawMessage) *Entry {
	text := valueText(raw)
	e := &Entry{Path: path, Keys: keys, key: NewText(path)}
	if len(text) > ValueCap {
		text, e.ValueCut = text[:ValueCap], true
	}
	e.value = []byte(strings.ToLower(text))
	if chars := util.ToChars(e.value); !chars.IsBytes() {
		e.wide = &Text{chars: chars}
	}
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
	Spread  bool
	ValueAt int
	onValue uint64
}

func (h Hit) valueTerm(i int) bool { return i < 64 && h.onValue&(1<<i) != 0 }

func (h Hit) KeyPositions(m *Matcher, query string) []int {
	var out []int
	for i, t := range Parse(query) {
		if !h.valueTerm(i) {
			out = append(out, t.keyPositions(m, h.Entry)...)
		}
	}
	sort.Ints(out)
	return out
}

func (h Hit) ValueMarks(m *Matcher, query, text string, from int) []int {
	var out []int
	for i, t := range Parse(query) {
		if h.valueTerm(i) {
			out = append(out, t.valueMarks(m, text, from)...)
		}
	}
	sort.Ints(out)
	return slices.Compact(out)
}

func (ix *Index) Query(m *Matcher, query string) []Hit {
	terms := Parse(query)
	positive := false
	for _, t := range terms {
		positive = positive || !t.negate
	}
	if !positive {
		ix.lastTerms, ix.lastPool = nil, nil
		return nil
	}
	pool := ix.flat
	if ix.lastTerms != nil && narrows(ix.lastTerms, terms) {
		pool = ix.lastPool
	}
	var hits []Hit
	var kept []*Entry
	tight := false
	for _, e := range pool {
		if h, ok := match(m, terms, e); ok {
			hits = append(hits, h)
			kept = append(kept, e)
			tight = tight || !h.Spread
		}
	}
	ix.lastTerms, ix.lastPool = terms, kept
	if tight {
		hits = slices.DeleteFunc(hits, func(h Hit) bool { return h.Spread })
	}
	slices.SortStableFunc(hits, Compare)
	return hits
}

func Compare(a, b Hit) int {
	if a.Spread != b.Spread {
		return boolOrder(!a.Spread, !b.Spread)
	}
	if a.OnKey != b.OnKey {
		return boolOrder(a.OnKey, b.OnKey)
	}
	return b.Score - a.Score
}

func match(m *Matcher, terms []Term, e *Entry) (Hit, bool) {
	h := Hit{Entry: e, ValueAt: -1}
	for i, t := range terms {
		score, onKey, spread := t.matchKey(m, e)
		var v valueHit
		if !onKey || spread {
			v = t.matchValue(m, e)
		}
		if t.negate {
			if onKey || v.ok {
				return Hit{}, false
			}
			continue
		}
		switch {
		case v.ok && (!onKey || !v.spread):
			if i < 64 {
				h.onValue |= 1 << i
			}
			h.Spread = h.Spread || v.spread
			h.Score += v.score
			if h.ValueAt < 0 || v.at < h.ValueAt {
				h.ValueAt = v.at
			}
		case onKey:
			h.OnKey = true
			h.Spread = h.Spread || spread
			h.Score += score
		default:
			return Hit{}, false
		}
	}
	return h, true
}

func (e *Entry) start(i int) int {
	off := 0
	for _, k := range e.Keys[:i] {
		off += len(k) + 1
	}
	return off
}

func (e *Entry) segment(i int) []byte {
	at := e.start(i)
	if e.key.chars.IsBytes() {
		return e.key.chars.Bytes()[at : at+len(e.Keys[i])]
	}
	return []byte(e.Path[at : at+len(e.Keys[i])])
}

func (e *Entry) segmentRunes(i int) int { return utf8.RuneCountInString(e.Path[:e.start(i)]) }

func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}
