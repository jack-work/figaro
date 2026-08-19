package tree

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Cache is a byte-budgeted window of materialized units over a durable
// substrate, per node, LRU by epoch, with an index that survives eviction.
// A HIT TAKES NO LOCK: everything a reader touches is immutable once published.
// Writers hold c.mu only to publish -- never across the Source, the budget's
// eviction pass, or the Evicted hook. See plans/tree-shaped-log.md.
type Cache[U any] struct {
	src    Source[U]
	size   Sizer[U]
	key    Keyer[U]
	budget *Budget

	// Evicted fires after a run is hollowed, outside all locks.
	Evicted func(Coord)

	// Recency is the layer-below's last-read epoch for a coord; eviction orders
	// by max(run epoch, oracle).
	Recency func(Coord) int64

	// nodes is an immutable map published by pointer.
	nodes atomic.Pointer[map[string]*node[U]]

	mu sync.Mutex // WRITERS ONLY

	recomposes atomic.Int64
}

// run is a contiguous materialized span within one node. Hollowing drops the
// units and keeps {coord, bytes}: the index that bounds the next miss.
// IMMUTABLE ONCE PUBLISHED, epoch excepted.
type run[U any] struct {
	coord    Coord
	units    []U // nil when hollow
	bytes    int64
	pinned   bool // cannot rematerialize; stays resident, stays counted
	resident bool
	epoch    atomic.Int64

	// dense: keys are base, base+1, ... with no holes. Checked once at
	// publication, which is sound only because a run is immutable; a dense run
	// is indexed with no Keyer call.
	dense bool
	base  uint64
}

// measure fills a run's bytes and density in one pass.
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

// node holds one node's runs, sorted by coord.From, published whole with a
// contiguous array of their upper bounds so locating a run touches one line.
type node[U any] struct {
	idx atomic.Pointer[runIndex[U]]
}

// runIndex is immutable once published; tos[i] == runs[i].coord.To.
type runIndex[U any] struct {
	runs []*run[U]
	tos  []uint64
}

// touch stores the current epoch, and only when stale: a per-read bump made
// reads slower with more readers.
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

// Close hands every counted byte back to the budget.
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

// RunInfo is one run of a node's index: what it covers, what it costs, and
// whether its units are here. The index survives eviction, so a tenant can ask
// what it holds without materializing anything.
type RunInfo struct {
	From, To uint64
	Bytes    int64
	Resident bool
}

// Index reports a node's runs, oldest coordinate first. Lock-free.
func (c *Cache[U]) Index(node string) []RunInfo {
	runs := c.runs(node)
	out := make([]RunInfo, 0, len(runs))
	for _, r := range runs {
		out = append(out, RunInfo{From: r.coord.From, To: r.coord.To, Bytes: r.bytes, Resident: r.resident})
	}
	return out
}

// Recomposes counts Source calls: climbing with reads rather than with
// distinct ranges means the window is too small for its load.
func (c *Cache[U]) Recomposes() int64 { return c.recomposes.Load() }

// Put seeds units the caller already holds. A Put at an existing run's exact
// coord replaces it and the budget is charged the delta. Pinned units stay
// resident and counted until Trim or Close.
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

// Drop hollows the run at exactly coord, pinned or not, and returns its bytes.
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

// Range returns the units in (from..to] along lineage, walking fork bases so
// branches share one copy of their common prefix. Misses rematerialize per gap.
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

// concat joins pieces with one allocation of the exact size.
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
func (c *Cache[U]) At(node string, idx uint64) (U, bool) {
	return atIn(c, c.runs(node), idx)
}

// ---- the node handle ----

// Handle is one node resolved once, so a tenant reading it repeatedly does not
// hash its name per read.
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

// At is Cache.At without the name lookup. A per-handle last-run hint was tried
// and reverted: it made parallel reads 6.5x worse (plans/tree-shaped-log.md).
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
	// Written out rather than sort.Search: a closure is an indirect call per probe.
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
	// A dense run needs no Keyer call.
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
func (c *Cache[U]) RangeAt(node string, from, to uint64) ([]U, error) {
	if to <= from {
		return nil, nil
	}
	return c.rangeInNode(Coord{Node: node, From: from, To: to})
}

// split maps (from..to] onto per-node coords by fork base; lineage is root-first.
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

// rangeInNode serves one node's coord, materializing gaps. The run slice is
// reloaded each step because a Source may run unlocked and publish successors.
func (c *Cache[U]) rangeInNode(coord Coord) ([]U, error) {
	return c.rangeInNodeAt(c.lookup(coord.Node), coord)
}

// rangeInNodeAt is rangeInNode with the node already resolved, so a handle
// does not re-hash the node's name on every call.
func (c *Cache[U]) rangeInNodeAt(nd *node[U], coord Coord) ([]U, error) {
	// One piece is the common answer and costs no copy; several allocate once.
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

// fill materializes a gap as new resident runs, chunked. The Source runs with
// no lock held.
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

		// Two callers may materialize one coord; the loser discards its result.
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

// refill rebuilds a hollow run as a successor at the same coord.
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

// fetch calls the Source with no lock held: a Source may re-enter this cache,
// and source_lock_test.go demonstrates the cycle when it does not.
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
	// A miss, never a panic: a desync must not crash the daemon.
	if lo > hi {
		return nil
	}
	return r.units[lo:hi]
}

// upperBound is the first index whose key exceeds target: an arithmetic guess
// that verifies both ends and falls back to a binary search. A dense run skips
// it entirely. Rationale and numbers: plans/tree-shaped-log.md.
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
			if (i == len(units) || c.key(units[i]) > target) &&
				(i == 0 || c.key(units[i-1]) <= target) {
				return i
			}
		}
	}
	return sort.Search(len(units), func(i int) bool { return c.key(units[i]) > target })
}

// runChunk bounds one run's span, so eviction has granularity.
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

// replace builds the successor slice with r at its coord, replacing an exact
// match or inserting in From order. Several runs may share a From, so the
// equal-From block is scanned rather than assumed to hold one entry.
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

// replaceIdentity swaps one run for its successor BY POINTER. Eviction and Drop
// use it: a coord lookup can miss, and a miss there leaves a run resident while
// the budget believes its bytes were freed.
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

// overlapping is the first run intersecting [pos, limit). Linear: To values do
// not ascend once a tenant Puts overlapping spans.
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

// hollow is the evicted successor of r: same coord and bytes, no units.
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

// coldest scans lock-free; evictColdest re-checks under c.mu.
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
