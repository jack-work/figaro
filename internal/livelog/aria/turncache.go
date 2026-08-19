package aria

import (
	"sort"
	"sync"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// The turn cache: the sealed section of an aria's UI IR, resident in THE
// canonical tree cache. plans/composed-layer-on-tree.md.

// TurnSource recomposes sealed turns from the durable log for an LT
// bracket, deltas included. It is the owner's half of a miss: the agent
// composes from its figLog, the reader from the backend.
type TurnSource func(fromLT, toLT uint64) []Turn

// ComposedCache is the residency of every aria's composed UI IR: one
// tree.Cache, a node per aria, one budget and one eviction order.
type ComposedCache struct {
	cache *fwtree.Cache[Turn]

	mu      sync.Mutex
	sources map[string]func(from, to uint64) []Turn
}

// NewComposedCache builds the process's composed cache against budget
// (nil is unbounded).
func NewComposedCache(budget *fwtree.Budget) *ComposedCache {
	cc := &ComposedCache{sources: map[string]func(from, to uint64) []Turn{}}
	cc.cache = fwtree.New[Turn](cc.fetch, budget, turnBytes, coordOf)
	return cc
}

// Budget is the accountant, for doctor mem and for a test that must wait.
func (cc *ComposedCache) Budget() *fwtree.Budget {
	if cc == nil {
		return nil
	}
	return cc.cache.Budget()
}

// Recomposes counts source calls across every node.
func (cc *ComposedCache) Recomposes() int64 {
	if cc == nil {
		return 0
	}
	return cc.cache.Recomposes()
}

func (cc *ComposedCache) register(node string, fill func(from, to uint64) []Turn) {
	cc.mu.Lock()
	cc.sources[node] = fill
	cc.mu.Unlock()
}

func (cc *ComposedCache) unregister(node string) {
	cc.mu.Lock()
	delete(cc.sources, node)
	cc.mu.Unlock()
}

func (cc *ComposedCache) fetch(co fwtree.Coord) ([]Turn, error) {
	cc.mu.Lock()
	fill := cc.sources[co.Node]
	cc.mu.Unlock()
	if fill == nil {
		return nil, nil
	}
	return fill(co.From, co.To), nil
}

// coordOf is the coordinate a turn is addressed by: the LT of the record
// that OPENED it. Turn brackets ascend and do not overlap, so the key
// space is sparse and ordered, which is what tree addresses.
//
// A turn no LT bracket can recompose is keyed in the reserved region
// above phantomBase, where no logical time reaches.
func coordOf(t Turn) uint64 {
	if len(t.LTs) > 0 && t.LTs[0] != 0 {
		return t.LTs[0]
	}
	return phantomCoord(t.ID)
}

// phantomBase reserves the top of the key space for turns nothing can
// recompose: a sealed turn whose records never reached the log (a round
// that failed before writing one). They are held pinned rather than
// dropped, because nothing below can serve them back.
const phantomBase = uint64(1) << 63

func phantomCoord(id uint64) uint64 { return phantomBase | id }

// DefaultUIWindowMB bounds composed UI IR, and it lives HERE because this is
// the package that holds those bytes. 16 MiB holds the working set of several
// large arias at once (the biggest measured composes to ~6 MB) while bounding
// an axis that was unbounded; a miss costs one range recompose (~ms), not a
// disk read. A caller with a configured number TUNES this; it does not supply
// it, for the reason store.DefaultIRBudgetBytes states.
const DefaultUIWindowMB = 16

// TurnCache is one aria's sealed section: an ordered key list, and the
// payloads in the shared tree. The NEWEST turn is not in the tree at all
// -- it is the staging slot the Server mutates in place (Close folds the
// open suffix in, Seal stamps the bracket), and a tree run is immutable
// once published. It enters the cache when a newer turn displaces it, by
// which time it is sealed and recomposable.
type TurnCache struct {
	keys []turnKey
	tail *Turn

	node   string
	shared *ComposedCache
	cache  *fwtree.Cache[Turn]
	source TurnSource
}

// turnKey is the index entry: what a page walk needs to plan without
// materializing anything. It is the tenant's key-space map, not a second
// residency policy -- no budget, no eviction order, no lock.
//
// lo is NON-DECREASING across the list, phantoms included (a phantom
// borrows its predecessor's end), so the list is binary-searchable.
type turnKey struct {
	id      uint64
	lo, hi  uint64 // the turn's LT bracket
	at      int64
	sealed  bool
	phantom bool // no bracket: addressed in the reserved key space
	bytes   int
}

// coord is where this turn's payload lives in the cache's key space.
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

// keyOf indexes a turn. prevHi is the previous entry's bracket end, which
// an unbracketed turn borrows so the list stays ordered.
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

// NewTurnCache returns a cache with its own private, unbounded node: a
// Server that is never bound keeps every sealed turn resident, which is
// what tests and one-shot callers expect. BindCache re-seats it on the
// process's shared cache.
func NewTurnCache(source TurnSource, shared *ComposedCache) *TurnCache {
	c := &TurnCache{node: "local", source: source}
	c.attach(c.node, shared)
	return c
}

// attach points this tenant at a cache and registers its half of a miss.
func (c *TurnCache) attach(node string, shared *ComposedCache) {
	c.node, c.shared = node, shared
	if shared != nil {
		c.cache = shared.cache
		shared.register(node, c.fill)
		return
	}
	c.cache = fwtree.New[Turn](func(co fwtree.Coord) ([]Turn, error) {
		return c.fill(co.From, co.To), nil
	}, nil, turnBytes, coordOf)
}

// bind re-seats this tenant on the shared cache under its aria's node.
func (c *TurnCache) bind(node string, shared *ComposedCache, source TurnSource) {
	held := c.materialized()
	c.release()
	c.keys, c.tail, c.source = nil, nil, source
	c.attach(node, shared)
	c.seed(held)
}

// fill answers a miss on this node: the bracket is snapped to WHOLE
// turns from the key list, because composing a partial turn drops the
// records that opened it, and the answer is clipped to the coord the
// cache asked for.
func (c *TurnCache) fill(from, to uint64) []Turn {
	if c.source == nil {
		return nil
	}
	lo, hi := c.bracket(from, to)
	if hi < lo {
		return nil
	}
	got := c.source(lo, hi)
	out := got[:0]
	for _, t := range got {
		if k := coordOf(t); k > from && k <= to {
			out = append(out, t)
		}
	}
	return out
}

// bracket is the LT span covering every turn whose coordinate falls in
// (from..to]: from the first such turn's opening LT to the last one's
// closing LT.
func (c *TurnCache) bracket(from, to uint64) (lo, hi uint64) {
	found := false
	for i := c.search(from); i < len(c.keys); i++ {
		k := c.keys[i]
		co := k.coord()
		if co <= from || k.phantom {
			continue
		}
		if co > to {
			break
		}
		if !found {
			lo, found = k.lo, true
		}
		hi = k.hi
	}
	if !found {
		return 1, 0
	}
	return lo, hi
}

// search is the first index whose bracket can reach past lt.
func (c *TurnCache) search(lt uint64) int {
	return sort.Search(len(c.keys), func(i int) bool { return c.keys[i].hi > lt })
}

// ---- the sealed-section surface the Server consumes ----

// Len is the number of sealed turns known (resident or not).
func (c *TurnCache) Len() int { return len(c.keys) }

// LastID is the newest turn's id, 0 when none.
func (c *TurnCache) LastID() uint64 {
	if len(c.keys) == 0 {
		return 0
	}
	return c.keys[len(c.keys)-1].id
}

// Tail returns the newest turn, or nil. It is the staging slot: always
// resident, mutated in place by the Server, never a published run.
func (c *TurnCache) Tail() *Turn { return c.tail }

// Append adds a turn at the tail, displacing the previous one into the
// cache.
func (c *TurnCache) Append(t Turn) {
	c.flushTail()
	c.keys = append(c.keys, keyOf(t, c.prevHi()))
	tc := t
	c.tail = &tc
}

// ReplaceAll adopts a fully-materialized history (boot, restore).
func (c *TurnCache) ReplaceAll(turns []Turn) {
	c.release()
	c.keys, c.tail = nil, nil
	c.attach(c.node, c.shared)
	c.seed(turns)
}

// seed puts every turn but the newest into the cache, cut into runs no
// larger than the cache's own target: a run larger than the budget can
// never stay resident.
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

func (c *TurnCache) put(from, to uint64, turns []Turn) {
	c.cache.Put(fwtree.Coord{Node: c.node, From: from, To: to}, turns, false)
}

// putPinned holds a turn nothing below can recompose. It is COUNTED --
// a meter that reads zero exactly when retention is worst is the worst
// possible meter (S1, plans/storm-triage.md) -- and pinned PER TURN, so
// one unrecomposable turn cannot disable eviction for anything else.
func (c *TurnCache) putPinned(t Turn) {
	k := phantomCoord(t.ID)
	c.cache.Put(fwtree.Coord{Node: c.node, From: k - 1, To: k}, []Turn{t}, true)
}

// flushTail moves the staging turn into the cache. Called when a newer
// turn displaces it, by which time it is sealed.
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
	// A RUN'S SPAN IS A KEY RANGE, not a record range: from the turn
	// before it to this turn's own key. The turn's records reach further,
	// but nothing addresses them here.
	c.put(c.keys[i].lo-1, c.keys[i].lo, []Turn{t})
}

// Slice materializes and returns turns[lo:hi+1] (indices into the sealed
// list), faulting whatever is missing back in through the source. A turn
// the source cannot return comes back as its index entry alone: the
// paginator sees an empty node list and steps over it, which degrades to
// a gap rather than a lie about content.
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

// rangeTurns reads keys[lo..hi] out of the cache, index-aligned: one
// Range for the ordinary span, one lookup per phantom-keyed turn, and a
// zero Turn wherever the source could not answer.
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
		}
	}
	return out
}

// lineage is this node's ancestry. One node today; the fork seam lands
// with the shared source (plans/composed-layer-on-tree.md).
func (c *TurnCache) lineage() []fwtree.Ref {
	return []fwtree.Ref{{Node: c.node}}
}

// materialized is every turn this tenant currently holds WITH CONTENT,
// for a re-seat: an index stub is not a turn and must not be seeded as
// one, or a later read would serve emptiness instead of faulting.
func (c *TurnCache) materialized() []Turn {
	if len(c.keys) == 0 {
		return nil
	}
	return c.Slice(0, len(c.keys)-1)
}

// IndexOf locates a turn id in the sealed list.
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

// ChunkFor picks the index range a byte-budgeted page walk needs around
// an anchor, from the SIZES in the index -- exact, not guessed -- with
// one extra turn of margin on each side so the page's More flags stay
// truthful at the window's edges.
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

// Recomposes reports how many source recompositions have run.
func (c *TurnCache) Recomposes() int { return int(c.cache.Recomposes()) }

// TailMutated re-tallies the staging turn's index entry after the Server
// mutated it in place (Close folding the suffix in, Seal stamping LTs,
// OpenInquiry).
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

// Release hands this aria's composed bytes back and stops answering
// misses for it. The server remains usable; its turns fault back in.
func (c *TurnCache) Release() { c.release() }

func (c *TurnCache) release() {
	if c.shared != nil {
		c.shared.unregister(c.node)
		c.cache.DropNode(c.node)
		return
	}
	if c.cache != nil {
		c.cache.Close()
	}
}

// ---- sizing ----

// turnBytes estimates a turn's resident cost: its node payloads plus the
// turn-level strings. An estimate at insert, like every other window in
// the process; do not sum reflect-walked struct sizes (that lied 3x low
// once already) -- string lengths dominate and are what this counts.
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
