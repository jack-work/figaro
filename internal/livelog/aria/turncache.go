package aria

import (
	"sort"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// Composer recomposes one node's sealed turns for an LT bracket. It must
// return whole turns: every turn whose opening record falls in the bracket,
// complete, and no others.
type Composer func(node string, fromLT, toLT uint64) []Turn

// ComposedCache holds every aria's composed UI IR: one tree.Cache, a node per
// aria, one budget.
type ComposedCache struct {
	cache   *fwtree.Cache[Turn]
	compose Composer

	// lineage names a node's ancestry, root first; nil makes every aria a root.
	lineage func(node string) []fwtree.Ref
}

// NewComposedCache: nil budget is unbounded; nil compose makes every miss a gap.
func NewComposedCache(budget *fwtree.Budget, compose Composer, lineage func(node string) []fwtree.Ref) *ComposedCache {
	cc := &ComposedCache{compose: compose, lineage: lineage}
	cc.cache = fwtree.New[Turn](cc.fetch, budget, turnBytes, coordOf)
	return cc
}

func (cc *ComposedCache) Budget() *fwtree.Budget {
	if cc == nil {
		return nil
	}
	return cc.cache.Budget()
}

func (cc *ComposedCache) Recomposes() int64 {
	if cc == nil {
		return 0
	}
	return cc.cache.Recomposes()
}

// fetch answers a miss, clipped to the coord.
func (cc *ComposedCache) fetch(co fwtree.Coord) ([]Turn, error) {
	if cc.compose == nil {
		return nil, nil
	}
	got := cc.compose(co.Node, co.From+1, co.To)
	out := got[:0]
	for _, t := range got {
		if k := coordOf(t); k > co.From && k <= co.To {
			out = append(out, t)
		}
	}
	return out, nil
}

// coordOf is the LT of the record that opened the turn; a turn no bracket
// can recompose is keyed above phantomBase.
func coordOf(t Turn) uint64 {
	if len(t.LTs) > 0 && t.LTs[0] != 0 {
		return t.LTs[0]
	}
	return phantomCoord(t.ID)
}

// phantomBase reserves the top of the key space for turns nothing can
// recompose; they are held pinned.
const phantomBase = uint64(1) << 63

func phantomCoord(id uint64) uint64 { return phantomBase | id }

// DefaultUIWindowMB bounds composed UI IR; a miss costs one range recompose.
const DefaultUIWindowMB = 16

// TurnCache is one aria's sealed section: an ordered key list, and the
// payloads in the shared tree. The newest turn is the staging slot, mutated in
// place and published only when a newer turn displaces it.
type TurnCache struct {
	keys []turnKey
	tail *Turn

	node   string
	shared *ComposedCache
	cache  *fwtree.Cache[Turn]
}

// turnKey is the index entry. lo is non-decreasing across the list, phantoms
// included (a phantom borrows its predecessor's end).
type turnKey struct {
	id      uint64
	lo, hi  uint64 // the turn's LT bracket
	at      int64
	sealed  bool
	phantom bool // no bracket: addressed in the reserved key space
	bytes   int
}

func (k turnKey) coord() uint64 {
	if k.phantom {
		return phantomCoord(k.id)
	}
	return k.lo
}

func (k turnKey) lts() []uint64 {
	if k.phantom {
		return nil
	}
	return []uint64{k.lo, k.hi}
}

// keyOf indexes a turn; an unbracketed turn borrows prevHi so the list stays ordered.
func keyOf(t Turn, prevHi uint64) turnKey {
	k := turnKey{id: t.ID, at: t.At, sealed: t.Sealed, bytes: turnBytes(t)}
	if len(t.LTs) > 0 && t.LTs[0] != 0 {
		k.lo, k.hi = t.LTs[0], t.LTs[0]
		if len(t.LTs) > 1 && t.LTs[1] > k.hi {
			k.hi = t.LTs[1]
		}
		return k
	}
	k.phantom = true
	k.lo, k.hi = prevHi, prevHi
	return k
}

func (c *TurnCache) prevHi() uint64 {
	if n := len(c.keys); n > 0 {
		return c.keys[n-1].hi
	}
	return 0
}

// NewTurnCache returns a cache on a private, unbounded node until bound.
func NewTurnCache(shared *ComposedCache) *TurnCache {
	c := &TurnCache{node: "local"}
	c.attach(c.node, shared)
	return c
}

func (c *TurnCache) attach(node string, shared *ComposedCache) {
	c.node, c.shared = node, shared
	if shared != nil {
		c.cache = shared.cache
		return
	}
	c.cache = fwtree.New[Turn](nil, nil, turnBytes, coordOf)
}

func (c *TurnCache) bind(node string, shared *ComposedCache) {
	held := c.materialized()
	c.release()
	c.keys, c.tail = nil, nil
	c.attach(node, shared)
	c.seed(held)
}

// Len is the number of turns known, resident or not.
func (c *TurnCache) Len() int { return len(c.keys) }

func (c *TurnCache) LastID() uint64 {
	if len(c.keys) == 0 {
		return 0
	}
	return c.keys[len(c.keys)-1].id
}

// Tail is the staging slot: always resident, mutated in place; nil when none.
func (c *TurnCache) Tail() *Turn { return c.tail }

// Append adds a turn at the tail, displacing the previous one into the cache.
func (c *TurnCache) Append(t Turn) {
	c.flushTail()
	c.keys = append(c.keys, keyOf(t, c.prevHi()))
	tc := t
	c.tail = &tc
}

// ReplaceAll adopts a fully-materialized history.
func (c *TurnCache) ReplaceAll(turns []Turn) {
	c.release()
	c.keys, c.tail = nil, nil
	c.attach(c.node, c.shared)
	c.seed(turns)
}

// seed puts every turn but the newest into the cache, cut at the run target.
func (c *TurnCache) seed(turns []Turn) {
	if len(turns) == 0 {
		return
	}
	for _, t := range turns {
		c.keys = append(c.keys, keyOf(t, c.prevHi()))
	}
	tail := turns[len(turns)-1]
	c.tail = &tail

	target := c.cache.RunTargetBytes()
	var batch []Turn
	bytes := 0
	var from uint64
	open := false
	flush := func(to uint64) {
		if len(batch) == 0 {
			return
		}
		c.put(from, to, batch)
		batch, bytes, from, open = nil, 0, to, false
	}
	for _, t := range turns[:len(turns)-1] {
		k := coordOf(t)
		if k >= phantomBase {
			c.putPinned(t)
			continue
		}
		if !open {
			if from == 0 {
				from = k - 1
			}
			open = true
		}
		batch = append(batch, t)
		bytes += turnBytes(t)
		if bytes >= target {
			flush(k)
		}
	}
	if len(batch) > 0 {
		flush(coordOf(batch[len(batch)-1]))
	}
}

// put publishes a run of this node's own turns; a turn below the fork base
// belongs to an ancestor's node.
func (c *TurnCache) put(from, to uint64, turns []Turn) {
	base := c.ownBase()
	for len(turns) > 0 && coordOf(turns[0]) < base {
		turns = turns[1:]
		if len(turns) > 0 {
			from = coordOf(turns[0]) - 1
		}
	}
	if len(turns) == 0 {
		return
	}
	c.cache.Put(fwtree.Coord{Node: c.node, From: from, To: to}, turns, false)
}

// putPinned holds a turn nothing below can recompose: counted, pinned per turn.
func (c *TurnCache) putPinned(t Turn) {
	k := phantomCoord(t.ID)
	c.cache.Put(fwtree.Coord{Node: c.node, From: k - 1, To: k}, []Turn{t}, true)
}

// flushTail moves the staging turn into the cache.
func (c *TurnCache) flushTail() {
	if c.tail == nil {
		return
	}
	t := *c.tail
	c.tail = nil
	i := len(c.keys) - 1
	if i < 0 {
		return
	}
	prev := uint64(0)
	if i > 0 {
		prev = c.keys[i-1].hi
	}
	c.keys[i] = keyOf(t, prev)
	if c.keys[i].phantom {
		c.putPinned(t)
		return
	}
	// A run's span is a key range, not a record range.
	c.put(c.keys[i].lo-1, c.keys[i].lo, []Turn{t})
}

// Slice materializes turns[lo:hi+1], faulting what is missing; a turn the
// source cannot return comes back as its index entry alone. Ids come from the
// key list: a bracket composed in isolation numbers from 1.
func (c *TurnCache) Slice(lo, hi int) []Turn {
	if lo < 0 {
		lo = 0
	}
	if hi > len(c.keys)-1 {
		hi = len(c.keys) - 1
	}
	if lo > hi {
		return nil
	}
	last := len(c.keys) - 1
	top := hi
	if c.tail != nil && top == last {
		top--
	}
	got := c.rangeTurns(lo, top)
	out := make([]Turn, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		if c.tail != nil && i == last {
			out = append(out, *c.tail)
			continue
		}
		if t := got[i-lo]; t.ID != 0 {
			out = append(out, t)
			continue
		}
		k := c.keys[i]
		out = append(out, Turn{ID: k.id, At: k.at, Sealed: k.sealed, LTs: k.lts()})
	}
	return out
}

// rangeTurns reads keys[lo..hi] out of the cache, index-aligned, with a zero
// Turn wherever the source could not answer.
func (c *TurnCache) rangeTurns(lo, hi int) []Turn {
	if hi < lo {
		return nil
	}
	out := make([]Turn, hi-lo+1)
	first, last := -1, -1
	for i := lo; i <= hi; i++ {
		if c.keys[i].phantom {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first >= 0 {
		got, _ := c.cache.Range(c.lineage(), c.keys[first].coord()-1, c.keys[last].coord())
		j := 0
		for i := first; i <= last; i++ {
			if c.keys[i].phantom {
				continue
			}
			co := c.keys[i].coord()
			for j < len(got) && coordOf(got[j]) < co {
				j++
			}
			if j < len(got) && coordOf(got[j]) == co {
				out[i-lo] = got[j]
				out[i-lo].ID = c.keys[i].id
				j++
			}
		}
	}
	for i := lo; i <= hi; i++ {
		if !c.keys[i].phantom {
			continue
		}
		if t, ok := c.cache.At(c.node, c.keys[i].coord()); ok {
			out[i-lo] = t
			out[i-lo].ID = c.keys[i].id
		}
	}
	return out
}

// lineage is this node's ancestry with every fork base snapped to a turn
// boundary: a base can fall inside a turn whose content differs between branches.
func (c *TurnCache) lineage() []fwtree.Ref {
	if c.shared == nil || c.shared.lineage == nil {
		return []fwtree.Ref{{Node: c.node}}
	}
	refs := c.shared.lineage(c.node)
	if len(refs) == 0 {
		return []fwtree.Ref{{Node: c.node}}
	}
	out := make([]fwtree.Ref, len(refs))
	copy(out, refs)
	for i := range out {
		out[i].Base = c.snapBase(out[i].Base)
	}
	return out
}

// snapBase lowers a fork base to the key of the turn it falls inside, so the
// straddling turn becomes the child's outright.
func (c *TurnCache) snapBase(base uint64) uint64 {
	if base == 0 || len(c.keys) == 0 {
		return base
	}
	i := sort.Search(len(c.keys), func(i int) bool { return c.keys[i].hi >= base })
	if i == len(c.keys) || c.keys[i].phantom {
		return base
	}
	if k := c.keys[i]; k.lo < base && base <= k.hi {
		return k.lo
	}
	return base
}

// ownBase is the coordinate below which turns live in an ancestor's node.
func (c *TurnCache) ownBase() uint64 {
	refs := c.lineage()
	if len(refs) < 2 {
		return 0
	}
	return refs[len(refs)-1].Base
}

func (c *TurnCache) materialized() []Turn {
	if len(c.keys) == 0 {
		return nil
	}
	return c.Slice(0, len(c.keys)-1)
}

func (c *TurnCache) IndexOf(id uint64) (int, bool) {
	lo, hi := 0, len(c.keys)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case c.keys[mid].id == id:
			return mid, true
		case c.keys[mid].id < id:
			lo = mid + 1
		default:
			hi = mid - 1
		}
	}
	return 0, false
}

// FloorIndex is the index of the first turn at or above floor; len(keys) when
// the floor is above every turn held.
func (c *TurnCache) FloorIndex(floor Anchor) int {
	return sort.Search(len(c.keys), func(i int) bool { return c.keys[i].id >= floor.Turn })
}

// ChunkFor picks the index range a byte-budgeted page walk needs around an
// anchor, with one turn of margin each side so More stays truthful.
func (c *TurnCache) ChunkFor(at Anchor, dir Direction, budget int) (lo, hi int) {
	n := len(c.keys)
	if n == 0 {
		return 0, -1
	}
	start := n - 1
	if !at.Zero() {
		if i, ok := c.IndexOf(at.Turn); ok {
			start = i
		} else if at.Turn < c.keys[0].id {
			start = 0
		}
	} else if dir == Forward {
		start = 0
	}
	lo, hi = start, start
	remaining := budget
	if dir == Forward {
		for hi+1 < n && remaining > 0 {
			hi++
			remaining -= c.keys[hi].bytes
		}
	} else {
		for lo > 0 && remaining > 0 {
			lo--
			remaining -= c.keys[lo].bytes
		}
	}
	if lo > 0 {
		lo--
	}
	if hi+1 < n {
		hi++
	}
	return lo, hi
}

func (c *TurnCache) Recomposes() int { return int(c.cache.Recomposes()) }

// TailMutated re-tallies the staging turn's index entry after an in-place edit.
func (c *TurnCache) TailMutated() {
	i := len(c.keys) - 1
	if i < 0 || c.tail == nil {
		return
	}
	prev := uint64(0)
	if i > 0 {
		prev = c.keys[i-1].hi
	}
	c.keys[i] = keyOf(*c.tail, prev)
}

func (c *TurnCache) Release() { c.release() }

func (c *TurnCache) release() {
	if c.shared != nil {
		c.cache.DropNode(c.node)
		return
	}
	if c.cache != nil {
		c.cache.Close()
	}
}

// turnBytes estimates a turn's resident cost by string length, which
// dominates; a reflect-walked struct size undercounts badly.
func turnBytes(t Turn) int {
	n := len(t.Inquiry)
	for _, s := range t.InquirySegments {
		n += len(s.Text) + len(s.Sender)
	}
	for k, d := range t.FormDeltas {
		n += len(k) + len(d.Value) + len(d.Form)
	}
	for _, nd := range t.Nodes {
		n += nodeSize(nd)
		for k, d := range nd.FormDeltas {
			n += len(k) + len(d.Value) + len(d.Form)
		}
	}
	return n
}
