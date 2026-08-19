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
			freed = r.bytes
			n.publish(replace(runs, hollow(r)))
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
func (c *Cache[U]) Range(lineage []Ref, from, to uint64) ([]U, error) {
	if len(lineage) == 0 || to < from {
		return nil, nil
	}
	var out []U
	// Split the ask across the lineage by fork bases, root first.
	for _, cut := range c.split(lineage, from, to) {
		units, err := c.rangeInNode(cut)
		out = append(out, units...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// At returns ONE unit at coordinate idx, or false if it is not resident. It
// NEVER calls the Source and NEVER allocates.
//
// WHY THIS EXISTS, MEASURED. The range surface returns a MATERIALIZED SLICE of
// everything it covers, so a per-record read through RangeAt allocated and
// copied the whole chunk to hand back one payload: 1153 B/op and 1 alloc/op
// against 3 B/op and 0 allocs for the tenant's own index, and 285-500 ns/op
// against 32-40. A read path that wants ONE record cannot use a surface shaped
// like a range without paying for the range.
//
// So the range door stays for callers that want a span, and this is the door
// for callers that want a record. Both are lock-free: runs and units are
// immutable once published.
func (c *Cache[U]) At(node string, idx uint64) (U, bool) {
	var zero U
	rs := c.runs(node)
	i := sort.Search(len(rs), func(i int) bool { return rs[i].coord.To >= idx })
	if i == len(rs) {
		return zero, false
	}
	r := rs[i]
	if r.coord.From >= idx || !r.resident {
		return zero, false
	}
	j := sort.Search(len(r.units), func(j int) bool { return c.key(r.units[j]) >= idx })
	if j < len(r.units) && c.key(r.units[j]) == idx {
		return r.units[j], true
	}
	return zero, false
}

// ResidentAt returns the units for (from..to] ONLY IF they are already
// resident, and NEVER calls the Source.
//
// THE DISTINCTION IS LOAD-BEARING, not a convenience. A caller that wants to
// EXTEND what is resident -- a writer keeping its own tail warm -- must not
// FAULT IT IN when it is absent: doing so re-creates residency the evictor
// just dropped, and an append loop racing a sweep then livelocks, each undoing
// the other. That is not hypothetical; it hung a test for 25 seconds.
//
// Lock-free: the runs slice and the units it names are immutable once
// published.
func (c *Cache[U]) ResidentAt(node string, from, to uint64) ([]U, bool) {
	if to <= from {
		return nil, false
	}
	for _, r := range c.runs(node) {
		if r.coord.From == from && r.coord.To == to && r.resident {
			return r.units, true
		}
	}
	return nil, false
}

// ResidentRuns counts runs holding units, across every node. It replaces a
// tenant counting its own copy of the index.
// ResidentRuns TAKES NO LOCK: every slice it walks is immutable once
// published, so the worst it can report is a count from an instant that has
// already passed -- which is what any count of a live cache is.
func (c *Cache[U]) ResidentRuns() int {
	n := 0
	for _, nd := range *c.nodes.Load() {
		for _, r := range nd.load() {
			if r.resident {
				n++
			}
		}
	}
	return n
}

// DropNode hollows every run of one node, returning their bytes to the budget.
// The OWNER saying gone, for a whole node at once: a segment closing, a file
// unlinked.
func (c *Cache[U]) DropNode(node string) {
	c.mu.Lock()
	var freed int64
	if nd := c.lookup(node); nd != nil {
		for _, r := range nd.load() {
			if r.resident {
				freed += r.bytes
				r.units = nil
				r.resident = false
				r.pinned = false
			}
		}
	}
	c.mu.Unlock()
	if c.budget != nil && freed > 0 {
		c.budget.bytes.Add(-freed)
	}
}

// RangeAt serves ONE NODE with no lineage, for a tenant whose data has no
// fork structure at this layer.
//
// A SEGMENT FILE HAS NO LINEAGE. Forks live at disk.Log, which delegates reads
// below a fork base to the parent LOG; by the time a read reaches one segment
// it is entirely that segment's. So the payload cache would have to build a
// one-element []Ref and walk split() on every hit -- two allocations and a
// loop to arrive at the coord it already knew. Range stays the door for
// lineage-shaped tenants; this is the door for the others.
func (c *Cache[U]) RangeAt(node string, from, to uint64) ([]U, error) {
	if to <= from {
		return nil, nil
	}
	return c.rangeInNode(Coord{Node: node, From: from, To: to})
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
	var out []U
	for pos := coord.From; pos < coord.To; {
		r := overlapping(c.runs(coord.Node), pos, coord.To)
		if r == nil {
			units, err := c.fill(Coord{coord.Node, pos, coord.To})
			return append(out, units...), err
		}
		if r.coord.From > pos { // a gap ahead of this run
			units, err := c.fill(Coord{coord.Node, pos, r.coord.From})
			out = append(out, units...)
			if err != nil {
				return out, err
			}
			pos = r.coord.From
			continue
		}
		if !r.resident {
			units, err := c.refill(coord.Node, r)
			if err != nil {
				return out, err
			}
			// Slice the LOCAL units rather than the run: the charge inside
			// refill may have evicted this very run again (a run larger than
			// the whole budget can never stay resident), and the caller must
			// still get what was fetched.
			out = append(out, sliceUnits(c.key, units, pos, coord.To)...)
		} else {
			r.touch(c)
			out = append(out, c.slice(r, pos, coord.To)...)
		}
		if r.coord.To <= pos {
			break // no progress; refuse to spin
		}
		pos = r.coord.To
	}
	return out, nil
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
func replace[U any](runs []*run[U], r *run[U]) []*run[U] {
	i := sort.Search(len(runs), func(i int) bool { return runs[i].coord.From >= r.coord.From })
	if i < len(runs) && runs[i].coord == r.coord {
		next := make([]*run[U], len(runs))
		copy(next, runs)
		next[i] = r
		return next
	}
	next := make([]*run[U], 0, len(runs)+1)
	next = append(next, runs[:i]...)
	next = append(next, r)
	return append(next, runs[i:]...)
}

func at[U any](runs []*run[U], coord Coord) *run[U] {
	i := sort.Search(len(runs), func(i int) bool { return runs[i].coord.From >= coord.From })
	if i < len(runs) && runs[i].coord == coord {
		return runs[i]
	}
	return nil
}

// overlapping is the first run intersecting [pos, limit).
func overlapping[U any](runs []*run[U], pos, limit uint64) *run[U] {
	i := sort.Search(len(runs), func(i int) bool { return runs[i].coord.To > pos })
	if i < len(runs) && runs[i].coord.From < limit {
		return runs[i]
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
	victimNode.publish(replace(victimNode.load(), hollow(victim)))
	hook := c.Evicted
	c.mu.Unlock()

	if hook != nil {
		hook(coord) // outside every lock: the layer below may lock itself
	}
	return freed
}
