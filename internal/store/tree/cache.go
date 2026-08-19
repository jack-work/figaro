package tree

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Cache is the window itself: per-node runs of materialized units, an
// index that survives eviction, prefix-shared residency across a
// lineage. One instance per layer; the type parameter is the unit.
//
// A HIT TAKES NO LOCK. Every structure a reader touches -- the node map, a
// node's run slice, a run and its units -- is IMMUTABLE ONCE PUBLISHED, and a
// writer builds a successor and stores a pointer to it. That is not a
// performance preference: it is what lets one uniform window serve a hot tail
// AND a cold range, which a mutex could not. Measured on this package's own
// benchmark, 2026-08-19: under the mutex a 64-unit Range cost 1.4 us serial
// and 2.3 us with readers -- it got SLOWER as readers were added, and that
// inversion is the entire reason a second, flat cache shape survived beside
// this one (plans/log-cache-policy.md, "LRU owns the cold ranges, never the
// hot tail").
//
// WRITERS still take c.mu, and it is a real lock with real concurrent callers:
// two arias materializing into one cache is the ordinary case. It is held ONLY
// to publish a successor -- never across a Source call, never across the
// budget's eviction pass, never across the Evicted hook. Each of those three
// is a call OUT of this package, and a lock held across a call out is the
// deadlock this stack has already met (docs/store/tree.md).
type Cache[U any] struct {
	src    Source[U]
	size   Sizer[U]
	key    Keyer[U]
	budget *Budget

	// Evicted, when set, fires after a run is hollowed (outside all
	// locks): the hook a lower layer uses to clear its lock-free fast
	// pointer. It receives the coord only; the units are already gone.
	Evicted func(Coord)

	// Recency, when set, is the layer-below's recency oracle: a coord's
	// LAST-READ epoch, maintained by that layer on its own lock-free
	// path (a segment stamps usedAt in nanoseconds; tree must not tax
	// that read with a lock). Eviction orders by max(run epoch, oracle).
	Recency func(Coord) int64

	// nodes is an immutable map published by pointer. Node CREATION copies it;
	// that happens once per node, while run publication (frequent) copies only
	// the one node's run slice.
	nodes atomic.Pointer[map[string]*node[U]]

	mu sync.Mutex // WRITERS ONLY

	recomposes atomic.Int64
}

// run is a contiguous materialized span within one node. Hollowing
// drops units and keeps {coord, bytes}: the index that makes the next
// miss BOUNDED.
//
// IMMUTABLE ONCE PUBLISHED, epoch excepted. A reader that is holding a run
// while it is evicted keeps serving the units it already has -- they are
// canonical bytes, not a view of something that changed -- and the run is
// collected when that reader lets go.
type run[U any] struct {
	coord    Coord
	units    []U // nil when hollow
	bytes    int64
	pinned   bool // cannot rematerialize; stays resident, stays counted
	resident bool
	epoch    atomic.Int64
}

// node holds one trunk node's runs, sorted by coord.From, published whole.
type node[U any] struct {
	runs atomic.Pointer[[]*run[U]]
}

// touch refreshes recency the cheap way: RECENCY IS AN EPOCH. The epoch
// advances only on load and sweep (rare); a touched run only STORES it,
// and only when stale. A bump per read was measured making reads slower
// with more readers (segment cache, 2026-08); the generic must not
// reintroduce what the concrete already paid to remove.
func (r *run[U]) touch(c *Cache[U]) {
	if e := c.budget.EpochNow(); r.epoch.Load() != e {
		r.epoch.Store(e)
	}
}

// New builds a cache over src, accounted against b (nil = unbounded).
func New[U any](src Source[U], b *Budget, size Sizer[U], key Keyer[U]) *Cache[U] {
	c := &Cache[U]{src: src, size: size, key: key, budget: b}
	empty := map[string]*node[U]{}
	c.nodes.Store(&empty)
	b.adopt(c)
	return c
}

// Close hands every counted byte back. An owner torn down without this
// poisons the accountant with ghosts (figaro learned this the hard way).
func (c *Cache[U]) Close() {
	c.mu.Lock()
	var freed int64
	for _, n := range *c.nodes.Load() {
		for _, r := range n.load() {
			if r.resident {
				freed += r.bytes
			}
		}
	}
	empty := map[string]*node[U]{}
	c.nodes.Store(&empty)
	c.mu.Unlock()
	if c.budget != nil {
		c.budget.bytes.Add(-freed)
		c.budget.disown(c)
	}
}

// Recomposes reports source calls to date, for the thrash alarm: a
// count climbing with READS rather than with distinct ranges means the
// window is too small for its load.
func (c *Cache[U]) Recomposes() int64 { return c.recomposes.Load() }

// Put seeds units the caller already holds (a seal, a decode already
// paid for), so the freshest data never costs a rematerialize. Pinned
// marks a unit range no Source can rebuild -- it stays resident and
// counted until Trim or Close. A Put at an existing run's exact coord
// REPLACES it (the writer's tail growing in place), and the budget is
// charged the delta.
func (c *Cache[U]) Put(coord Coord, units []U, pinned bool) {
	r := &run[U]{coord: coord, units: append([]U(nil), units...), pinned: pinned, resident: true}
	r.epoch.Store(c.bump())
	for _, u := range units {
		r.bytes += int64(c.size(u))
	}
	delta := r.bytes

	c.mu.Lock()
	n := c.nodeLocked(coord.Node)
	old := n.load()
	if prev := at(old, coord); prev != nil && prev.resident {
		delta -= prev.bytes
	}
	n.publish(replace(old, r))
	c.mu.Unlock()

	c.budget.charge(delta)
}

// Drop hollows the run at exactly coord (a sealed segment released, a
// node deleted), returning its bytes to the budget. Pinned runs drop
// too: Drop is the OWNER saying gone, not the sweep asking.
func (c *Cache[U]) Drop(coord Coord) {
	c.mu.Lock()
	var freed int64
	if n := c.lookup(coord.Node); n != nil {
		runs := n.load()
		if r := at(runs, coord); r != nil && r.resident {
			if next, ok := replaceIdentity(runs, r, hollow(r)); ok {
				freed = r.bytes
				n.publish(next)
			}
		}
	}
	c.mu.Unlock()
	if c.budget != nil && freed > 0 {
		c.budget.bytes.Add(-freed)
	}
}

// Range returns the units in (from..to] along lineage, walking fork
// bases: the portion below each child's Base is served from -- and
// becomes resident in -- the ANCESTOR's node, so branches share one
// copy of their common prefix. Misses rematerialize per contiguous gap.
//
// THE RESULT MAY ALIAS THE CACHE. Where one run answers the whole span the
// caller is handed A VIEW OF THAT RUN'S UNITS, which is the point: a cache
// that copies its answer is a cache of the work needed to produce a copy. The
// units are therefore READ-ONLY TO THE CALLER, exactly as the substrate's own
// bytes are. Aliasing is safe against eviction by construction -- a run is
// immutable once published and eviction publishes a HOLLOW SUCCESSOR, so a
// holder keeps serving what it already has and the memory is collected when it
// lets go.
func (c *Cache[U]) Range(lineage []Ref, from, to uint64) ([]U, error) {
	if len(lineage) == 0 || to < from {
		return nil, nil
	}
	// ONE CUT IS THE COMMON CASE -- an unforked aria, or a read entirely
	// above the last fork base -- and it hands back the node's answer
	// UNCOPIED. Concatenating a single piece into a fresh slice is a copy of
	// the whole span for nothing.
	// A SINGLE-NODE LINEAGE ASKS FOR NO SPLIT AT ALL. Every tenant that is not
	// reading across a fork base is in this case, and building a one-element
	// []Coord to hand it to the same walk is an allocation per read.
	if len(lineage) == 1 {
		return c.rangeInNode(Coord{Node: lineage[0].Node, From: from, To: to})
	}
	cuts := c.split(lineage, from, to)
	if len(cuts) == 1 {
		return c.rangeInNode(cuts[0])
	}
	pieces := make([][]U, 0, len(cuts))
	total := 0
	for _, cut := range cuts {
		units, err := c.rangeInNode(cut)
		pieces = append(pieces, units)
		total += len(units)
		if err != nil {
			return concat(pieces, total), err
		}
	}
	return concat(pieces, total), nil
}

// concat joins pieces with ONE allocation of the exact size. Appending piece
// by piece regrows the destination as it goes: measured on the hot tail read,
// the growth was 4 allocations and 3.5x the bytes of the flat window's single
// make+copy.
func concat[U any](pieces [][]U, total int) []U {
	if len(pieces) == 0 || total == 0 {
		return nil
	}
	if len(pieces) == 1 {
		return pieces[0]
	}
	out := make([]U, 0, total)
	for _, p := range pieces {
		out = append(out, p...)
	}
	return out
}

// split maps (from..to] onto per-node coords by fork bases. lineage is
// root-first; child i's own records begin at lineage[i].Base.
func (c *Cache[U]) split(lineage []Ref, from, to uint64) []Coord {
	var cuts []Coord
	lo := from
	for i, ref := range lineage {
		hi := to
		if i+1 < len(lineage) && lineage[i+1].Base > 0 && lineage[i+1].Base-1 < hi {
			hi = lineage[i+1].Base - 1 // below the next child's base: ancestor's
		}
		if hi > lo {
			cuts = append(cuts, Coord{Node: ref.Node, From: lo, To: hi})
			lo = hi
		}
		if lo >= to {
			break
		}
	}
	return cuts
}

// rangeInNode serves one node's coord, materializing gaps.
//
// THE RUN SLICE IS RELOADED EVERY STEP, and that is not defensive: another
// caller publishes a successor while this one is inside a Source, so a slice
// captured once would walk an index that no longer describes the node. The
// runs it names stay valid -- they are immutable -- so the reload costs a
// pointer load and gains an up-to-date index.
func (c *Cache[U]) rangeInNode(coord Coord) ([]U, error) {
	// PIECES, NOT AN APPEND. A span served by ONE resident run -- the hot tail,
	// and every read smaller than a chunk -- then costs NO COPY AT ALL: the
	// caller gets a view of the run's own units. Where several runs answer,
	// concat allocates once at the exact size instead of regrowing.
	// The first piece is held in a local: the overwhelmingly common answer is
	// ONE piece, and a [][]U for it is an allocation per read on the hot path.
	var first []U
	var pieces [][]U
	total := 0
	add := func(u []U) {
		if len(u) == 0 {
			return
		}
		total += len(u)
		if first == nil {
			first = u
			return
		}
		if pieces == nil {
			pieces = append(pieces, first)
		}
		pieces = append(pieces, u)
	}
	joined := func() []U {
		if pieces == nil {
			return first
		}
		return concat(pieces, total)
	}
	for pos := coord.From; pos < coord.To; {
		r := overlapping(c.runs(coord.Node), pos, coord.To)
		if r == nil {
			units, err := c.fill(Coord{coord.Node, pos, coord.To})
			add(units)
			return joined(), err
		}
		if r.coord.From > pos { // a gap ahead of this run
			units, err := c.fill(Coord{coord.Node, pos, r.coord.From})
			add(units)
			if err != nil {
				return joined(), err
			}
			pos = r.coord.From
			continue
		}
		if !r.resident {
			units, err := c.refill(coord.Node, r)
			if err != nil {
				return joined(), err
			}
			// Slice the LOCAL units rather than the run: the charge inside
			// refill may have evicted this very run again (a run larger than
			// the whole budget can never stay resident), and the caller must
			// still get what was fetched.
			add(sliceUnits(c.key, units, pos, coord.To))
		} else {
			r.touch(c)
			add(c.slice(r, pos, coord.To))
		}
		if r.coord.To <= pos {
			break // no progress; refuse to spin
		}
		pos = r.coord.To
	}
	return joined(), nil
}

// fill materializes a gap as NEW resident runs, chunked, and returns what it
// fetched. THE SOURCE RUNS WITH NO LOCK HELD.
func (c *Cache[U]) fill(coord Coord) ([]U, error) {
	var all []U
	for lo := coord.From; lo < coord.To; {
		hi := lo + runChunk
		if hi > coord.To {
			hi = coord.To
		}
		cc := Coord{Node: coord.Node, From: lo, To: hi}
		units, err := c.fetch(cc)
		if err != nil {
			return all, err
		}

		r := &run[U]{coord: cc, units: units, resident: true}
		r.epoch.Store(c.bump())
		for _, u := range units {
			r.bytes += int64(c.size(u))
		}

		// TWO CALLERS MAY MATERIALIZE THE SAME COORD, and the loser discards
		// its result: one wasted Source call rather than a lock held across
		// I/O. The same trade the segment range unit priced when it dropped
		// loadMu.
		c.mu.Lock()
		n := c.nodeLocked(cc.Node)
		runs := n.load()
		if existing := at(runs, cc); existing != nil && existing.resident {
			c.mu.Unlock()
			all = append(all, sliceUnits(c.key, existing.units, cc.From, cc.To)...)
			lo = hi
			continue
		}
		n.publish(replace(runs, r))
		c.mu.Unlock()

		c.budget.charge(r.bytes)
		all = append(all, units...)
		lo = hi
	}
	return all, nil
}

// refill rebuilds a hollow run in place -- a successor at the same coord --
// and returns the units it fetched.
func (c *Cache[U]) refill(name string, r *run[U]) ([]U, error) {
	units, err := c.fetch(r.coord)
	if err != nil {
		return nil, err
	}
	next := &run[U]{coord: r.coord, units: units, resident: true, pinned: r.pinned}
	next.epoch.Store(c.bump())
	for _, u := range units {
		next.bytes += int64(c.size(u))
	}

	c.mu.Lock()
	n := c.nodeLocked(name)
	runs := n.load()
	if cur := at(runs, r.coord); cur != nil && cur.resident {
		// Another caller refilled it; theirs is already charged.
		c.mu.Unlock()
		return cur.units, nil
	}
	n.publish(replace(runs, next))
	c.mu.Unlock()

	c.budget.charge(next.bytes)
	return units, nil
}

// fetch calls the Source with NO LOCK HELD.
//
// THE COMMENT THAT STOOD HERE WAS THE JUSTIFICATION FOR A DEFECT, and it is
// recorded rather than deleted because it is the finding. It read: "fetch
// calls the source ... under c.mu; the source reads a lower layer with its own
// locking -- THE LAYERS FORM A DAG, NEVER A CYCLE, WHICH IS WHAT MAKES THIS
// SAFE." Nothing tested that claim, and source_lock_test.go demonstrates the
// cycle in three seconds. A layer below that consults this same window (a fork
// reading its parent's prefix, a composed layer asking the decoded one)
// reaches it without anybody intending to.
func (c *Cache[U]) fetch(coord Coord) ([]U, error) {
	c.recomposes.Add(1)
	if c.src == nil {
		return nil, nil
	}
	return c.src(coord)
}

func (c *Cache[U]) slice(r *run[U], from, to uint64) []U {
	if to <= from {
		return nil
	}
	lo := sort.Search(len(r.units), func(i int) bool { return c.key(r.units[i]) > from })
	hi := sort.Search(len(r.units), func(i int) bool { return c.key(r.units[i]) > to })
	// A MISS, NEVER A PANIC. lo > hi is only reachable if from > to, which the
	// guard above refuses -- but a slice expression that can panic on a
	// bookkeeping desync turns a wrong answer into a crashed daemon, and this
	// one did (slice bounds out of range [15:13], TestConcurrentRange).
	if lo > hi {
		return nil
	}
	return r.units[lo:hi]
}

// runChunk bounds one run's span so eviction has granularity: a gap
// larger than this becomes several runs, and no single run can exceed
// the budget by construction of its span (bytes may still vary; the
// guarantee is granularity, not equality).
const runChunk = 64

// sliceUnits is slice over a local units slice by key bracket.
func sliceUnits[U any](key Keyer[U], units []U, from, to uint64) []U {
	lo := 0
	for lo < len(units) && key(units[lo]) <= from {
		lo++
	}
	hi := lo
	for hi < len(units) && key(units[hi]) <= to {
		hi++
	}
	return units[lo:hi]
}

// ---- the published index ----

func (n *node[U]) load() []*run[U] {
	if p := n.runs.Load(); p != nil {
		return *p
	}
	return nil
}

func (n *node[U]) publish(runs []*run[U]) { n.runs.Store(&runs) }

// runs is the lock-free read of one node's index.
func (c *Cache[U]) runs(name string) []*run[U] {
	if n := c.lookup(name); n != nil {
		return n.load()
	}
	return nil
}

func (c *Cache[U]) lookup(name string) *node[U] { return (*c.nodes.Load())[name] }

// nodeLocked returns the node, creating it by publishing a copied map.
// Caller holds c.mu.
func (c *Cache[U]) nodeLocked(name string) *node[U] {
	cur := *c.nodes.Load()
	if n := cur[name]; n != nil {
		return n
	}
	next := make(map[string]*node[U], len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	n := &node[U]{}
	next[name] = n
	c.nodes.Store(&next)
	return n
}

// replace builds the successor slice with r at its coord: an exact-coord
// replacement, or an insertion keeping the sort by From. O(runs) in the copy,
// which is what the old in-place insert already paid.
//
// SEVERAL RUNS MAY SHARE A From. A tenant that widens its tail Puts (a..b] and
// then (a..b+1], so an index keyed only by From holds two runs at one starting
// coordinate; the equal-From block is scanned rather than assumed to be one
// entry. Found by TestRangeAnswersItsSpanUnderRandomResidency: with the
// assumption in place, eviction INSERTED a hollow duplicate instead of
// replacing its victim, the victim stayed resident, coldest kept returning it,
// and TrimIdle spun until the test timed out.
func replace[U any](runs []*run[U], r *run[U]) []*run[U] {
	i := sort.Search(len(runs), func(i int) bool { return runs[i].coord.From >= r.coord.From })
	for j := i; j < len(runs) && runs[j].coord.From == r.coord.From; j++ {
		if runs[j].coord == r.coord {
			next := make([]*run[U], len(runs))
			copy(next, runs)
			next[j] = r
			return next
		}
	}
	next := make([]*run[U], 0, len(runs)+1)
	next = append(next, runs[:i]...)
	next = append(next, r)
	return append(next, runs[i:]...)
}

// replaceIdentity swaps ONE run for its successor BY POINTER. Eviction and
// Drop use this rather than replace: a coord lookup can miss, and a miss there
// does not fail loudly -- it leaves the victim resident while the budget
// believes its bytes were freed, which is a meter that lies in the safe-looking
// direction.
func replaceIdentity[U any](runs []*run[U], old, next *run[U]) ([]*run[U], bool) {
	for i, r := range runs {
		if r == old {
			out := make([]*run[U], len(runs))
			copy(out, runs)
			out[i] = next
			return out, true
		}
	}
	return runs, false
}

func at[U any](runs []*run[U], coord Coord) *run[U] {
	i := sort.Search(len(runs), func(i int) bool { return runs[i].coord.From >= coord.From })
	for ; i < len(runs) && runs[i].coord.From == coord.From; i++ {
		if runs[i].coord == coord {
			return runs[i]
		}
	}
	return nil
}

// overlapping is the first run intersecting [pos, limit), scanned in order.
//
// LINEAR, AND DELIBERATELY. Runs are sorted by From, but their To values need
// not ascend once a tenant Puts overlapping spans, so a binary search on To
// answers about an order the index does not have. The locked walk this replaced
// scanned too; the scan is over ONE node's runs, which chunking bounds.
func overlapping[U any](runs []*run[U], pos, limit uint64) *run[U] {
	for _, r := range runs {
		if r.coord.From >= limit {
			break // sorted by From: nothing later can intersect
		}
		if r.coord.To > pos {
			return r
		}
	}
	return nil
}

// hollow is the evicted successor of r: same coord, same bytes on the index,
// no units.
func hollow[U any](r *run[U]) *run[U] {
	h := &run[U]{coord: r.coord, bytes: r.bytes}
	h.epoch.Store(r.epoch.Load())
	return h
}

func (c *Cache[U]) bump() int64 {
	if c.budget == nil {
		return 0
	}
	return c.budget.epoch.Add(1)
}

// ---- the owner half of the accountant ----

func (c *Cache[U]) effEpoch(r *run[U]) int64 {
	e := r.epoch.Load()
	if c.Recency != nil {
		if o := c.Recency(r.coord); o > e {
			e = o
		}
	}
	return e
}

// coldest scans lock-free: every slice it walks is immutable, so the worst a
// concurrent publish costs is a victim chosen from an index one step old, and
// evictColdest re-checks under c.mu before hollowing anything.
func (c *Cache[U]) coldest() (int64, bool) {
	best, found := int64(0), false
	for _, n := range *c.nodes.Load() {
		for _, r := range n.load() {
			if r.resident && !r.pinned {
				if e := c.effEpoch(r); !found || e < best {
					best, found = e, true
				}
			}
		}
	}
	return best, found
}

func (c *Cache[U]) evictColdest() int64 {
	c.mu.Lock()
	var victim *run[U]
	var victimNode *node[U]
	var victimE int64
	for _, n := range *c.nodes.Load() {
		for _, r := range n.load() {
			if r.resident && !r.pinned {
				if e := c.effEpoch(r); victim == nil || e < victimE {
					victim, victimNode, victimE = r, n, e
				}
			}
		}
	}
	if victim == nil {
		c.mu.Unlock()
		return 0
	}
	freed := victim.bytes
	coord := victim.coord
	next, ok := replaceIdentity(victimNode.load(), victim, hollow(victim))
	if !ok {
		// The victim left the index while we were choosing it. Free nothing:
		// whoever removed it accounted for it.
		c.mu.Unlock()
		return 0
	}
	victimNode.publish(next)
	hook := c.Evicted
	c.mu.Unlock()

	if hook != nil {
		hook(coord) // outside every lock: the layer below may lock itself
	}
	return freed
}
