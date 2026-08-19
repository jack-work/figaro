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

	// dense says the keys of units are base, base+1, ... with no holes, and
	// base is units[0]'s key. CHECKED ONCE, AT PUBLICATION, and true forever
	// after -- WHICH IS ONLY SOUND BECAUSE A RUN IS IMMUTABLE. A dense run is
	// indexed arithmetically with NO Keyer call at all, where a search costs
	// about six and even a verified guess costs one or two, and the Keyer is a
	// function value in a struct field so none of them inlines.
	//
	// The check is one pass over units at materialization, which is already
	// O(units) -- it rides along with the loop that sums their bytes.
	dense bool
	base  uint64
}

// measure fills a run's bytes, and its density, in the single pass that has to
// walk the units anyway.
func (c *Cache[U]) measure(r *run[U]) {
	r.bytes = 0
	r.dense = len(r.units) > 0
	for i, u := range r.units {
		r.bytes += int64(c.size(u))
		k := c.key(u)
		if i == 0 {
			r.base = k
		} else if r.dense && k != r.base+uint64(i) {
			r.dense = false
		}
	}
}

// node holds one trunk node's runs, sorted by coord.From, published whole --
// together with a CONTIGUOUS ARRAY OF THEIR UPPER BOUNDS.
//
// WHY THE SECOND ARRAY, and it is not a cache of anything: locating a run is a
// binary search, and searching []*run[U] dereferences a pointer to a separate
// heap object at every probe. Searching []uint64 touches one contiguous line.
// The profile of the segment payload path put 93% of the remaining lookup time
// in that search once the unit lookup had been made arithmetic. The structure
// this tenant deleted held its runs as VALUES in one slice, which is the same
// property expressed by layout instead of by an index.
//
// It is published in the same store as the runs, so a reader cannot see one
// without the other.
type node[U any] struct {
	idx atomic.Pointer[runIndex[U]]
}

// runIndex is a node's runs and their upper bounds, immutable once published.
// tos[i] == runs[i].coord.To.
type runIndex[U any] struct {
	runs []*run[U]
	tos  []uint64
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
	c.measure(r)
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
	return atIn(c, c.runs(node), idx)
}

// ---- the node handle ----

// Handle is one node, resolved ONCE.
//
// A TENANT THAT READS ONE NODE TEN THOUSAND TIMES MUST NOT HASH THAT NODE'S
// NAME TEN THOUSAND TIMES. Every string-keyed accessor on Cache hashes a node
// name before it can do anything -- for the segment payload cache that is a
// filesystem path, hashed once per record read, and it was MEASURED as the
// whole of a 2.2x serial regression once allocation was removed (85 ns/op
// against 39, 0 allocs both sides).
//
// The name is a NAMING cost and naming happens at open. Nothing is added to
// any index and no complexity changes: O(1) to O(1). A Handle carries the
// *node and the methods the tenant already called, and nothing else.
type Handle[U any] struct {
	c    *Cache[U]
	n    *node[U]
	name string
}

// Node resolves a name to a handle, creating the node if it is new.
func (c *Cache[U]) Node(name string) *Handle[U] {
	c.mu.Lock()
	n := c.nodeLocked(name)
	c.mu.Unlock()
	return &Handle[U]{c: c, n: n, name: name}
}

// At is Cache.At without the name lookup.
//
// A PER-HANDLE "LAST RUN" HINT WAS TRIED HERE AND REVERTED, 2026-08-19. The
// profile said 93% of the remaining search time was the binary search over
// RUNS, and a verified hint removes that search for a reader that stays inside
// one run. It made the SERIAL hit no better and the PARALLEL hit SIX AND A HALF
// TIMES WORSE -- 4.9 ns to 32 ns -- because every reader then writes the same
// atomic on every miss, which is a shared cache line under contention.
//
// That is the law this package was founded on, in its own package comment:
// RECENCY IS AN EPOCH, because "a per-read atomic stamp on a shared line made
// reads SLOWER with more readers". I had quoted it that morning and
// reintroduced it by lunch. A read path may GUESS, but it may not REMEMBER.
func (h *Handle[U]) At(idx uint64) (U, bool) {
	ix := h.n.index()
	if ix == nil {
		var zero U
		return zero, false
	}
	u, ok, _ := atInIndex(h.c, ix, idx)
	return u, ok
}

// Range is Cache.RangeAt without the name lookup.
func (h *Handle[U]) Range(from, to uint64) ([]U, error) {
	if to <= from {
		return nil, nil
	}
	return h.c.rangeInNodeAt(h.n, Coord{Node: h.name, From: from, To: to})
}

// ResidentAt is Cache.ResidentAt without the name lookup.
func (h *Handle[U]) ResidentAt(from, to uint64) ([]U, bool) {
	return residentIn(nodeRuns(h.n), from, to)
}

// Put and DropNode are miss-path and teardown; they keep the name because they
// are not hot and the coord carries it anyway.
func (h *Handle[U]) Put(from, to uint64, units []U, pinned bool) {
	h.c.Put(Coord{Node: h.name, From: from, To: to}, units, pinned)
}

func (h *Handle[U]) Drop() { h.c.DropNode(h.name) }

// nodeRuns is the immutable run slice of a node, or nil.
func nodeRuns[U any](n *node[U]) []*run[U] {
	if n == nil {
		return nil
	}
	return n.load()
}

func residentIn[U any](rs []*run[U], from, to uint64) ([]U, bool) {
	for _, r := range rs {
		if r.coord.From == from && r.coord.To == to && r.resident {
			return r.units, true
		}
	}
	return nil, false
}

func atIn[U any](c *Cache[U], rs []*run[U], idx uint64) (U, bool) {
	tos := make([]uint64, len(rs))
	for i, r := range rs {
		tos[i] = r.coord.To
	}
	u, ok, _ := atInIndex(c, &runIndex[U]{runs: rs, tos: tos}, idx)
	return u, ok
}

// atInWhere is atIn plus WHICH RUN answered, so a caller can remember it. -1
// when no run covers idx.
func atInIndex[U any](c *Cache[U], ix *runIndex[U], idx uint64) (U, bool, int) {
	var zero U
	// A CONTIGUOUS SEARCH, WRITTEN OUT. tos[i] is runs[i].coord.To, so this
	// probes one packed array instead of chasing a pointer per step -- and it
	// is hand-written rather than sort.Search because sort.Search takes a
	// CLOSURE, which is an indirect call per probe that cannot inline. The
	// profile put a third of this lookup in that closure after the unit
	// lookup had been made arithmetic.
	tos := ix.tos
	lo, hi := 0, len(tos)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tos[mid] < idx {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	i := lo
	rs := ix.runs
	if i == len(rs) {
		return zero, false, -1
	}
	r := rs[i]
	if r.coord.From >= idx || !r.resident {
		return zero, false, -1
	}
	// A DENSE RUN NEEDS NO KEYER AT ALL: density was checked once when the run
	// was published and a published run never changes.
	if r.dense {
		j := int(idx - r.base)
		if j >= 0 && j < len(r.units) {
			return r.units[j], true, i
		}
		return zero, false, i
	}
	j := c.upperBound(r.units, idx-1)
	if j < len(r.units) && c.key(r.units[j]) == idx {
		return r.units[j], true, i
	}
	return zero, false, i
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
	return residentIn(c.runs(node), from, to)
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
	return c.rangeInNodeAt(c.lookup(coord.Node), coord)
}

// rangeInNodeAt is rangeInNode with the node already resolved, so a handle
// does not re-hash the node's name on every call.
func (c *Cache[U]) rangeInNodeAt(nd *node[U], coord Coord) ([]U, error) {
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
		r := overlapping(nodeRuns(nd), pos, coord.To)
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
		c.measure(r)

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
	c.measure(next)

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
	lo := c.upperBoundRun(r, from)
	hi := c.upperBoundRun(r, to)
	// A MISS, NEVER A PANIC. lo > hi is only reachable if from > to, which the
	// guard above refuses -- but a slice expression that can panic on a
	// bookkeeping desync turns a wrong answer into a crashed daemon, and this
	// one did (slice bounds out of range [15:13], TestConcurrentRange).
	if lo > hi {
		return nil
	}
	return r.units[lo:hi]
}

// upperBound is the first index whose key exceeds target: a GUESS THAT VERIFIES
// ITSELF, falling back to a binary search when the guess is wrong.
//
// WHY A GUESS AT ALL. The keys of a run are usually DENSE -- one unit per
// coordinate, ascending, no holes -- because that is what a substrate handing
// back consecutive records produces. Where that holds, the answer is
// arithmetic: target - key(units[0]) + 1. A binary search over 64 units instead
// calls the Keyer about six times, and the Keyer is a FUNCTION VALUE IN A
// STRUCT FIELD, so every call is indirect and none of them inlines. Measured by
// fd15d2a0 on the segment payload path: that search is the whole of a 1.7x
// regression against a structure that indexed arithmetically.
//
// WHY IT VERIFIES RATHER THAN TRUSTING A DECLARATION. Density is a property of
// the TENANT'S KEY SPACE, not of this cache: Source may legally return fewer
// units than its coord names ("a hole degrades to a gap, never a lie"), and a
// decoded-IR key skips values whenever an entry is ceremonial or filtered. One
// hole and an unchecked arithmetic index returns A DIFFERENT RECORD'S CONTENT,
// which is the silent-wrong-answer failure this stack fears most. So the guess
// is CHECKED -- one comparison, against the six a search would cost -- and a
// failed check falls through to the search. A tenant cannot lie about density
// because nobody is asked.
//
// Gluck approved this shape over a declared-dense flag, 2026-08-19, on exactly
// that reasoning: "wouldn't the index be correct in every case?" -- it would
// not, and the check is what makes it so.
func (c *Cache[U]) upperBoundRun(r *run[U], target uint64) int {
	if r.dense {
		if target < r.base {
			return 0
		}
		if i := int(target-r.base) + 1; i <= len(r.units) {
			return i
		}
		return len(r.units)
	}
	return c.upperBound(r.units, target)
}

func (c *Cache[U]) upperBound(units []U, target uint64) int {
	if len(units) == 0 {
		return 0
	}
	base := c.key(units[0])
	if target >= base {
		if i := int(target-base) + 1; i >= 0 && i <= len(units) {
			// The guess is right when the unit BEFORE i is the last one at or
			// below target and the unit AT i is past it. Checking both ends is
			// what makes a hole anywhere in between unable to fool it: a hole
			// shifts every later key down, so the unit at i would carry a key
			// no greater than target.
			if (i == len(units) || c.key(units[i]) > target) &&
				(i == 0 || c.key(units[i-1]) <= target) {
				return i
			}
		}
	}
	return sort.Search(len(units), func(i int) bool { return c.key(units[i]) > target })
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
	if ix := n.idx.Load(); ix != nil {
		return ix.runs
	}
	return nil
}

func (n *node[U]) index() *runIndex[U] { return n.idx.Load() }

func (n *node[U]) publish(runs []*run[U]) {
	tos := make([]uint64, len(runs))
	for i, r := range runs {
		tos[i] = r.coord.To
	}
	n.idx.Store(&runIndex[U]{runs: runs, tos: tos})
}

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
