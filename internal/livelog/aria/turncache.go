package aria

import (
	"sort"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// The turn cache: the sealed section of an aria's UI IR, resident in THE
// canonical tree cache. plans/composed-layer-on-tree.md.

// Composer recomposes ONE node's sealed turns for an LT bracket, deltas
// included. It must return WHOLE turns: every turn whose OPENING record
// falls in the bracket, complete, and no others. A bracket that cuts a
// turn at either end is its problem, not its caller's.
type Composer func(node string, fromLT, toLT uint64) []Turn

// ComposedCache is the residency of every aria's composed UI IR: one
// tree.Cache, a node per aria, one budget and one eviction order.
type ComposedCache struct {
	cache   *fwtree.Cache[Turn]
	compose Composer

	// lineage names a node's ancestry, root first. nil makes every aria a
	// root.
	lineage func(node string) []fwtree.Ref
}

// NewComposedCache builds the process's composed cache against budget
// (nil is unbounded). compose may be nil, which makes every miss a gap.
func NewComposedCache(budget *fwtree.Budget, compose Composer, lineage func(node string) []fwtree.Ref) *ComposedCache {
	cc := &ComposedCache{compose: compose, lineage: lineage}
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

// fetch answers a miss, CLIPPED TO THE COORD.
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

// coordOf is the coordinate a turn is addressed by: the LT of the record
// that OPENED it. Turn brackets ascend and do not overlap. A turn no LT
// bracket can recompose is keyed above phantomBase.
func coordOf(t Turn) uint64 {
	if len(t.LTs) > 0 && t.LTs[0] != 0 {
		return t.LTs[0]
	}
	return phantomCoord(t.ID)
}

// phantomBase reserves the top of the key space for turns nothing can
// recompose: a sealed turn whose records never reached the log. They are
// held pinned; nothing below can serve them back.
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
// payloads in the shared tree. The NEWEST turn is the staging slot --
// mutated in place by the Server, never a published run -- and it enters
// the cache when a newer turn displaces it.
type TurnCache struct {
	keys []turnKey
	tail *Turn

	node   string
	shared *ComposedCache
	cache  *fwtree.Cache[Turn]
}

// turnKey is the index entry: what a page walk needs to plan without
// materializing anything. lo is NON-DECREASING across the list, phantoms
// included (a phantom borrows its predecessor's end).
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

// NewTurnCache returns a cache on its own private, unbounded node.
// BindCache re-seats it on the process's shared cache.
func NewTurnCache(shared *ComposedCache) *TurnCache {
	c := &TurnCache{node: "local"}
	c.attach(c.node, shared)
	return c
}

// attach points this tenant at a cache. Unshared, it gets a private
// unbounded node with no source: it holds what it was given and faults
// nothing.
func (c *TurnCache) attach(node string, shared *ComposedCache) {
	c.node, c.shared = node, shared
	if shared != nil {
		c.cache = shared.cache
		return
	}
	c.cache = fwtree.New[Turn](nil, nil, turnBytes, coordOf)
}

// bind re-seats this tenant on the shared cache under its aria's node.
func (c *TurnCache) bind(node string, shared *ComposedCache) {
	held := c.materialized()
	c.release()
	c.keys, c.tail = nil, nil
	c.attach(node, shared)
	c.seed(held)
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

// seed puts every turn but the newest into the cache, cut at the cache's
// own run target.
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

// put publishes a run of this node's OWN turns. A turn below the fork
// base belongs to an ancestor's node and is read through the lineage.
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

// putPinned holds a turn nothing below can recompose: counted, and
// pinned PER TURN (S1, plans/storm-triage.md).
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

// Slice materializes turns[lo:hi+1] (indices into the sealed list),
// faulting what is missing. A turn the source cannot return comes back as
// its index entry alone: a gap, never a lie about content.
//
// THE ID COMES FROM THE KEY LIST. A composer derives ids by counting
// openers in the records it was handed (turns.StampIDs), so a bracket
// composed in isolation numbers from 1 -- and a third of the arias in a
// real store carry no persisted ids to override it. The coordinate is the
// turn's opening LT; the index knows the number and the fault does not.
// TestAFaultDoesNotRenumberATurn.
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

// lineage is this node's ancestry with every fork base SNAPPED TO A TURN
// BOUNDARY. A base can fall inside a turn (ForkAt and ForkWith take
// interior LTs), and that turn's content differs between the branches.
// Asserted by TestAForkBelowATurnBoundaryServesItsOwnContent.
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

// snapBase lowers a fork base to the KEY of the turn it falls inside. A
// base is the FIRST coordinate the child owns (store's forkbase test), so
// the straddling turn becomes the child's outright.
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

// ownBase is the coordinate below which this node's turns are not its
// own: they live in an ancestor's node and are read through the lineage.
func (c *TurnCache) ownBase() uint64 {
	refs := c.lineage()
	if len(refs) < 2 {
		return 0
	}
	return refs[len(refs)-1].Base
}

// materialized is every turn this tenant holds, for a re-seat.
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

// Release hands this aria's composed bytes back.
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
