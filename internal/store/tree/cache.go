package tree

import (
	"sort"
	"sync"
)

// Cache is the window itself: per-node runs of materialized units, an
// index that survives eviction, prefix-shared residency across a
// lineage. One instance per layer; the type parameter is the unit.
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

	mu    sync.Mutex
	nodes map[string]*node[U]

	recomposes int64
}

// run is a contiguous materialized span within one node. Hollowing
// drops units and keeps {coord, bytes}: the index that makes the next
// miss BOUNDED.
type run[U any] struct {
	coord    Coord
	units    []U // nil when hollow
	bytes    int64
	epoch    int64
	pinned   bool // cannot rematerialize; stays resident, stays counted
	resident bool
}

type node[U any] struct{ runs []*run[U] } // sorted by coord.From

// touch refreshes recency the cheap way: RECENCY IS AN EPOCH. The epoch
// advances only on load and sweep (rare); a touched run only STORES it,
// and only when stale. A bump per read was measured making reads slower
// with more readers (segment cache, 2026-08); the generic must not
// reintroduce what the concrete already paid to remove.
func (r *run[U]) touch(c *Cache[U]) {
	if e := c.budget.EpochNow(); r.epoch != e {
		r.epoch = e
	}
}

// New builds a cache over src, accounted against b (nil = unbounded).
func New[U any](src Source[U], b *Budget, size Sizer[U], key Keyer[U]) *Cache[U] {
	c := &Cache[U]{src: src, size: size, key: key, budget: b, nodes: map[string]*node[U]{}}
	b.adopt(c)
	return c
}

// Close hands every counted byte back. An owner torn down without this
// poisons the accountant with ghosts (figaro learned this the hard way).
func (c *Cache[U]) Close() {
	c.mu.Lock()
	var freed int64
	for _, n := range c.nodes {
		for _, r := range n.runs {
			if r.resident {
				freed += r.bytes
			}
		}
	}
	c.nodes = map[string]*node[U]{}
	c.mu.Unlock()
	if c.budget != nil {
		c.budget.bytes.Add(-freed)
		c.budget.disown(c)
	}
}

// Recomposes reports source calls to date, for the thrash alarm: a
// count climbing with READS rather than with distinct ranges means the
// window is too small for its load.
func (c *Cache[U]) Recomposes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recomposes
}

// Put seeds units the caller already holds (a seal, a decode already
// paid for), so the freshest data never costs a rematerialize. Pinned
// marks a unit range no Source can rebuild -- it stays resident and
// counted until Trim or Close. A Put at an existing run's exact coord
// REPLACES it (the writer's tail growing in place), and the budget is
// charged the delta.
func (c *Cache[U]) Put(coord Coord, units []U, pinned bool) {
	c.mu.Lock()
	n := c.node(coord.Node)
	r := &run[U]{coord: coord, units: append([]U(nil), units...), pinned: pinned, resident: true}
	r.epoch = c.bump()
	for _, u := range units {
		r.bytes += int64(c.size(u))
	}
	delta := r.bytes
	if old := n.at(coord); old != nil {
		if old.resident {
			delta -= old.bytes
		}
		*old = *r
	} else {
		n.insert(r)
	}
	c.mu.Unlock()
	c.budget.charge(delta)
}

// Drop hollows the run at exactly coord (a sealed segment released, a
// node deleted), returning its bytes to the budget. Pinned runs drop
// too: Drop is the OWNER saying gone, not the sweep asking.
func (c *Cache[U]) Drop(coord Coord) {
	c.mu.Lock()
	var freed int64
	if r := c.node(coord.Node).at(coord); r != nil && r.resident {
		freed = r.bytes
		r.units = nil
		r.resident = false
		r.pinned = false
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
	cuts := c.split(lineage, from, to)
	for _, cut := range cuts {
		units, err := c.rangeInNode(cut)
		if err != nil {
			return out, err
		}
		out = append(out, units...)
	}
	return out, nil
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
func (c *Cache[U]) rangeInNode(coord Coord) ([]U, error) {
	c.mu.Lock()
	n := c.node(coord.Node)
	var out []U
	pos := coord.From
	// RE-SCAN n.runs EACH STEP RATHER THAN RANGING OVER IT.
	//
	// Materialization now RELEASES c.mu (fetchUnlocked), so another caller may
	// insert runs into this node while we are outside the lock. A `range
	// n.runs` captures the slice header ONCE and would then walk a stale array
	// -- and the desync shows up as `pos` overtaking coord.To and slice()
	// panicking with "slice bounds out of range [15:13]". THE EXISTING
	// TestConcurrentRange CAUGHT EXACTLY THAT under -race -count=2, before this
	// change was believed.
	//
	// Re-scanning from pos is O(runs) per step instead of O(runs) per call.
	// That is the cost of a Source that may run unlocked, and it is paid on a
	// path that is already doing I/O.
	for pos < coord.To {
		var r *run[U]
		for _, cand := range n.runs {
			if cand.coord.To <= pos || cand.coord.From >= coord.To {
				continue
			}
			r = cand
			break
		}
		if r == nil {
			break
		}
		// Gap before this run?
		if r.coord.From > pos {
			units, err := c.materializeLocked(n, Coord{coord.Node, pos, r.coord.From})
			if err != nil {
				c.mu.Unlock()
				return out, err
			}
			out = append(out, units...)
		}
		if !r.resident {
			units, err := c.materializeRunLocked(r)
			if err != nil {
				c.mu.Unlock()
				return out, err
			}
			// Slice the LOCAL units, not the run: the charge inside
			// materialize may have evicted this very run again (a run
			// larger than the whole budget can never stay resident),
			// and the caller must still get what was fetched.
			r.epoch = c.bump()
			out = append(out, sliceUnits(c.key, units, pos, coord.To)...)
			if r.coord.To > pos {
				pos = r.coord.To
			} else {
				break // no progress; refuse to spin
			}
			continue
		}
		r.touch(c)
		out = append(out, c.slice(r, pos, coord.To)...)
		if r.coord.To > pos {
			pos = r.coord.To
		} else {
			break // no progress; refuse to spin
		}
	}
	if pos < coord.To {
		units, err := c.materializeLocked(n, Coord{coord.Node, pos, coord.To})
		if err != nil {
			c.mu.Unlock()
			return out, err
		}
		out = append(out, units...)
	}
	c.mu.Unlock()
	return out, nil
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

// materializeLocked fills a gap as NEW resident runs, chunked.
func (c *Cache[U]) materializeLocked(n *node[U], coord Coord) ([]U, error) {
	var all []U
	for lo := coord.From; lo < coord.To; {
		hi := lo + runChunk
		if hi > coord.To {
			hi = coord.To
		}
		cc := Coord{Node: coord.Node, From: lo, To: hi}
		units, err := c.fetchUnlocked(cc)
		if err != nil {
			return all, err
		}
		// THE LOCK WAS RELEASED, SO THE RUN LIST MAY HAVE MOVED. Another
		// caller missing the same coord may have installed it while we were
		// materializing.
		//
		// WHAT CHANGES, NAMED RATHER THAN DISCOVERED: under a held lock one
		// caller waited and one won, so a coord was materialized exactly once.
		// Now BOTH MAY MATERIALIZE and one result is discarded -- two racing
		// misses cost ONE WASTED SOURCE CALL, never a double charge and never
		// a duplicate run. The same trade the segment range unit already
		// priced when it dropped loadMu.
		if existing := n.at(cc); existing != nil && existing.resident {
			all = append(all, sliceUnits(c.key, existing.units, cc.From, cc.To)...)
			lo = hi
			continue
		}
		r := &run[U]{coord: cc, units: units, resident: true, epoch: c.bump()}
		for _, u := range units {
			r.bytes += int64(c.size(u))
		}
		n.insert(r)
		c.chargeLocked(r.bytes)
		all = append(all, units...)
		lo = hi
	}
	return all, nil
}

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

// materializeRunLocked refills a hollow run in place. THE SOURCE RUNS
// UNLOCKED; see fetchUnlocked.
func (c *Cache[U]) materializeRunLocked(r *run[U]) ([]U, error) {
	units, err := c.fetchUnlocked(r.coord)
	if err != nil {
		return nil, err
	}
	// The lock was released, so another caller may have refilled this run
	// meanwhile. Theirs is already charged; discard ours rather than charge
	// the same bytes twice.
	if r.resident {
		return r.units, nil
	}
	r.units = units
	r.bytes = 0
	for _, u := range units {
		r.bytes += int64(c.size(u))
	}
	r.resident = true
	c.chargeLocked(r.bytes)
	return units, nil
}

// THE COMMENT THAT STOOD HERE WAS THE JUSTIFICATION FOR THE DEFECT, and it is
// recorded rather than deleted because it is the finding. It read: "fetch
// calls the source ... under c.mu; the source reads a lower layer with its own
// locking -- THE LAYERS FORM A DAG, NEVER A CYCLE, WHICH IS WHAT MAKES THIS
// SAFE."
//
// Nothing tested that claim, and source_lock_test.go now demonstrates the
// cycle in three seconds. A COMMENT IS A CLAIM NOBODY TESTS -- and this one
// asserted the exact property that made the design safe.
//
// fetchUnlocked RELEASES c.mu around the caller-supplied Source and re-takes
// it, exactly as chargeLocked already does around the budget's eviction pass.
//
// A SOURCE MUST NOT RUN UNDER c.mu. docs/store/tree.md states the rule for the
// other direction -- an Evicted hook takes no lock, because eviction fires
// under a consumer's lock and a hook that needs it deadlocks, "the shape that
// reaches production first" -- and the Source call is the same inversion
// running the other way. A layer below that consults this same window (a fork
// reading its parent's prefix, a composed layer asking the decoded one)
// reaches it without anybody intending to.
//
// It is not live today only because the sole tenant passes src = nil. It
// becomes live the moment a Source is installed, which is what the
// consolidation does.
func (c *Cache[U]) fetchUnlocked(coord Coord) ([]U, error) {
	c.recomposes++
	src := c.src
	if src == nil {
		return nil, nil
	}
	c.mu.Unlock()
	units, err := src(coord)
	c.mu.Lock()
	return units, err
}

// chargeLocked releases c.mu around the budget's eviction pass, because
// the budget may pick THIS cache as its victim and evictColdest takes
// c.mu. Re-entrancy by unlock, the simplest correct shape.
func (c *Cache[U]) chargeLocked(delta int64) {
	c.mu.Unlock()
	c.budget.charge(delta)
	c.mu.Lock()
}

func (c *Cache[U]) node(name string) *node[U] {
	n := c.nodes[name]
	if n == nil {
		n = &node[U]{}
		c.nodes[name] = n
	}
	return n
}

func (c *Cache[U]) bump() int64 {
	if c.budget == nil {
		return 0
	}
	return c.budget.epoch.Add(1)
}

func (n *node[U]) at(coord Coord) *run[U] {
	i := sort.Search(len(n.runs), func(i int) bool { return n.runs[i].coord.From >= coord.From })
	if i < len(n.runs) && n.runs[i].coord == coord {
		return n.runs[i]
	}
	return nil
}

func (n *node[U]) insert(r *run[U]) {
	i := sort.Search(len(n.runs), func(i int) bool { return n.runs[i].coord.From >= r.coord.From })
	n.runs = append(n.runs, nil)
	copy(n.runs[i+1:], n.runs[i:])
	n.runs[i] = r
}

// ---- the owner half of the accountant ----

func (c *Cache[U]) effEpoch(r *run[U]) int64 {
	e := r.epoch
	if c.Recency != nil {
		if o := c.Recency(r.coord); o > e {
			e = o
		}
	}
	return e
}

func (c *Cache[U]) coldest() (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	best, found := int64(0), false
	for _, n := range c.nodes {
		for _, r := range n.runs {
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
	var victimE int64
	for _, n := range c.nodes {
		for _, r := range n.runs {
			if r.resident && !r.pinned {
				if e := c.effEpoch(r); victim == nil || e < victimE {
					victim, victimE = r, e
				}
			}
		}
	}
	if victim == nil {
		c.mu.Unlock()
		return 0
	}
	freed := victim.bytes
	victim.units = nil
	victim.resident = false
	coord := victim.coord
	hook := c.Evicted
	c.mu.Unlock()
	if hook != nil {
		hook(coord) // outside every lock: the layer below may lock itself
	}
	return freed
}
