package segment

// The payload cache, re-seated on tree.Cache -- the ONE window shape
// this stack shares (see tree's package comment and docs/store/tree.md).
//
// THE CACHE UNIT IS A RANGE OF RECORDS, NOT A WHOLE SEGMENT. It used to be
// one indivisible unit per file: coordOf returned Coord{path, 0, 1}, the Keyer
// was the constant 1, and -- the load-bearing one -- the Cache was built with
// a NIL Source, so tree's Range, its per-gap rematerialization and its
// hollow-keeps-the-index property were not merely unused, THEY WERE
// UNREACHABLE from this tenant. A miss therefore ran readAllPayloads over the
// entire file: 200 ReadFrame calls and 104,000 bytes to answer for ONE record
// of a 200-record segment, measured.
//
// WHAT STAYS HERE, and it is the property tree must not tax: THE LOCK-FREE
// READ PATH. A hit is one atomic load plus a binary search over an IMMUTABLE
// slice of runs -- O(log K) where K is the resident runs of this segment, and
// no lock at any point. tree owns budget, eviction order (through the Recency
// oracle) and the sweep, and clears runs through the Evicted hook, outside
// every lock.
//
// WHY IMMUTABLE, AND IT IS NOT A MATTER OF TASTE. docs/store/tree.md: an
// Evicted hook that takes a lock DEADLOCKS -- a consumer calling Put under its
// own write lock can have eviction pick one of its own runs, so the hook runs
// with that lock held, "and only under budget pressure with concurrent
// readers, which is the shape that reaches production first". So clearing one
// run's share must be A POINTER SWAP PUBLISHING A SUCCESSOR. That constraint
// and the constraint that keeps the hit lock-free ARE THE SAME CONSTRAINT:
// copy-on-write is the only legal shape, and it is also the fast one.

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/tree"
)

// chunkRecords bounds a materialized range. A miss reads the ALIGNED chunk
// containing the record asked for, so a sequential scan materializes each
// chunk exactly once and never re-reads a record -- the traversal the fig IR
// encode path performs on every turn, and the one a naive range unit would
// ruin while fixing the single-record case.
const chunkRecords = 32

// unit is one record's payload with the coordinate tree needs to slice by.
// A bare []byte cannot answer the Keyer.
type unit struct {
	idx uint64 // 1-based: record i has coordinate i+1, matching (From..To]
	b   []byte
}

// run is a contiguous materialized span of records, [from, to) in 0-based
// record indices. IMMUTABLE ONCE PUBLISHED.
type run struct {
	from, to uint64
	payloads [][]byte
	bytes    int64
}

// residency is the whole of a segment's resident state: runs sorted by from,
// disjoint, IMMUTABLE. Eviction and extension both publish a SUCCESSOR rather
// than mutating this one, which is what lets the read path take no lock.
type residency struct{ runs []run }

// find is the read fast path's search: O(log K) over an immutable slice.
func (r *residency) find(i uint64) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	k := sort.Search(len(r.runs), func(n int) bool { return r.runs[n].to > i })
	if k == len(r.runs) || r.runs[k].from > i {
		return nil, false
	}
	return r.runs[k].payloads[i-r.runs[k].from], true
}

// with returns a successor holding rn, replacing any run at the same start.
func (r *residency) with(rn run) *residency {
	next := &residency{}
	if r != nil {
		next.runs = make([]run, 0, len(r.runs)+1)
		for _, e := range r.runs {
			if e.from == rn.from {
				continue // replaced (the writer's tail growing in place)
			}
			next.runs = append(next.runs, e)
		}
	}
	next.runs = append(next.runs, rn)
	sort.Slice(next.runs, func(a, b int) bool { return next.runs[a].from < next.runs[b].from })
	return next
}

// without returns a successor with the run starting at from removed.
//
// AN EMPTY RESIDENCY IS NIL, NOT AN EMPTY SLICE. Publishing &residency{} for a
// segment holding nothing makes "is anything resident?" answer YES to every
// pointer check, and the fast path then pays a search to learn there is
// nothing. The idle-sweep test caught exactly that: "a block nobody read
// survived three sweeps" -- the run HAD been evicted and the bytes returned,
// and only the pointer lied.
func (r *residency) without(from uint64) *residency {
	if r == nil {
		return nil
	}
	next := &residency{runs: make([]run, 0, len(r.runs))}
	for _, e := range r.runs {
		if e.from == from {
			continue
		}
		next.runs = append(next.runs, e)
	}
	if len(next.runs) == 0 {
		return nil
	}
	return next
}

// sliceHeaderBytes charges the per-record bookkeeping so a segment of
// many tiny records is not accounted as free.
const sliceHeaderBytes = 24

// defaultCacheBudget matches the pre-tree default: 32 MiB.
const defaultCacheBudget = 32 << 20

var (
	payloadBudget = tree.NewBudget(defaultCacheBudget)
	payloadCache  = newPayloadCache()

	loads atomic.Int64

	// registry maps a coord back to its Segment, for the Evicted hook
	// and the Recency oracle. MANY-TO-ONE now: several coords of one
	// segment. Guarded by regMu; consulted on eviction and sweep (rare),
	// never on the read fast path.
	regMu    sync.Mutex
	registry = map[tree.Coord]*Segment{}
)

func newPayloadCache() *tree.Cache[unit] {
	c := tree.New[unit](nil, payloadBudget,
		func(u unit) int { return len(u.b) + sliceHeaderBytes },
		func(u unit) uint64 { return u.idx })
	c.Evicted = func(coord tree.Coord) {
		regMu.Lock()
		s := registry[coord]
		delete(registry, coord)
		regMu.Unlock()
		if s == nil {
			return
		}
		// A POINTER SWAP AND NOTHING ELSE. No lock is taken here, by the
		// rule in docs/store/tree.md: this hook can fire with a consumer's
		// own write lock held.
		for {
			old := s.resident.Load()
			next := old.without(coord.From)
			if s.resident.CompareAndSwap(old, next) {
				return
			}
		}
	}
	c.Recency = func(coord tree.Coord) int64 {
		regMu.Lock()
		s := registry[coord]
		regMu.Unlock()
		if s == nil {
			return 0
		}
		return s.usedAt.Load()
	}
	return c
}

// coordFor names one materialized range of this segment. From/To are the
// record-index bounds under tree's (From..To] convention, so the units it
// holds are keyed From+1..To.
func (s *Segment) coordFor(from, to uint64) tree.Coord {
	return tree.Coord{Node: s.path, From: from, To: to}
}

// SetCacheBudget bounds the total bytes of segment payloads held in
// memory across every open log. Zero disables caching entirely (every
// read becomes a pread); a negative value is ignored.
func SetCacheBudget(bytes int64) {
	if bytes < 0 {
		return
	}
	payloadBudget.SetLimit(bytes)
	if bytes == 0 {
		payloadBudget.TrimIdle(-1)
	}
}

// CacheBudget reports the configured bound.
func CacheBudget() int64 { _, limit, _ := payloadBudget.Stats(); return limit }

// CachedBytes reports the payload bytes currently resident.
func CachedBytes() int64 { resident, _, _ := payloadBudget.Stats(); return resident }

// CacheLoads reports RANGE materializations to date: the thrash alarm.
// Climbing with READS rather than with distinct ranges means runs are
// being dropped as fast as they are built.
//
// THE UNIT OF THIS NUMBER CHANGED WITH THE CACHE UNIT. It counted
// whole-segment loads; it now counts range loads, so it climbs faster for the
// same workload and the two are NOT comparable across this commit. It is
// surfaced to users as segment_cache_loads (rpc) and printed by doctor.
func CacheLoads() int64 { return loads.Load() }

// CachedRanges reports how many resident ranges exist across all segments.
//
// RENAMED FROM CachedSegments, WHICH WOULD HAVE BECOME A LIE: registry is
// many-to-one now, so len(registry) counts RUNS. A number whose meaning
// changes under its own name is this stack's rename hazard in a metric.
func CachedRanges() int {
	regMu.Lock()
	defer regMu.Unlock()
	return len(registry)
}

// cachedPayload returns record i's payload if it is resident.
//
// THE FAST PATH TAKES NO LOCK AND CONSULTS tree NOT AT ALL: one atomic load,
// one binary search over an immutable slice, and at most one stale-epoch
// store. Routing this through tree.Range would put a process-wide mutex on the
// hottest path in the read stack; it is not done, it was not approved, and the
// Evicted rule above forbids the shape that would require it.
func (s *Segment) cachedPayload(i uint64) ([]byte, bool) {
	if res := s.resident.Load(); res != nil {
		if p, ok := res.find(i); ok {
			if e := payloadBudget.EpochNow(); s.usedAt.Load() != e {
				s.usedAt.Store(e)
			}
			return p, true
		}
	}
	if CacheBudget() <= 0 {
		return nil, false
	}
	// MISS: materialize the ALIGNED chunk containing i, never the file.
	lo := (i / chunkRecords) * chunkRecords
	hi := lo + chunkRecords
	if hi > s.count {
		hi = s.count
	}
	rn, units, err := s.readRange(lo, hi)
	if err != nil {
		return nil, false
	}
	loads.Add(1)

	// PUBLISH A SUCCESSOR. No loadMu: the CAS is the synchronization, and two
	// racing misses on one chunk cost one wasted read rather than a lock on
	// the read path. THE STANDING TEST -- what invariant spans this critical
	// section that could not be published as one immutable value? -- answers
	// NONE, so the lock was protecting a mutation that is now a replacement.
	for {
		old := s.resident.Load()
		if p, ok := old.find(i); ok {
			return p, true // someone else published it
		}
		if s.resident.CompareAndSwap(old, old.with(rn)) {
			break
		}
	}
	coord := s.coordFor(lo, hi)
	regMu.Lock()
	registry[coord] = s
	regMu.Unlock()
	s.usedAt.Store(payloadBudget.EpochNow())
	payloadCache.Put(coord, units, false)
	return rn.payloads[i-rn.from], true
}

// SweepIdle drops every run not read since `keep` sweeps ago and
// advances the epoch: tree.TrimIdle with the segments' own usedAt as
// the recency oracle.
func SweepIdle(keep int64) (dropped int, freed int64) {
	if keep < 0 {
		return 0, 0
	}
	return payloadBudget.TrimIdle(keep)
}

// readRange reads records [from, to) -- and NOTHING ELSE. This is the whole
// of the miss-cost change: O(records in range) ReadFrame calls where
// readAllPayloads made O(records in segment).
func (s *Segment) readRange(from, to uint64) (run, []unit, error) {
	rn := run{from: from, to: to, payloads: make([][]byte, 0, to-from)}
	units := make([]unit, 0, to-from)
	for i := from; i < to; i++ {
		off := s.offsets[i]
		nextOff := s.size
		if int(i)+1 < len(s.offsets) {
			nextOff = s.offsets[i+1]
		}
		payload, _, err := s.codec.ReadFrame(s.f, off, nextOff)
		if err != nil {
			return run{}, nil, err
		}
		rn.payloads = append(rn.payloads, payload)
		rn.bytes += int64(len(payload)) + sliceHeaderBytes
		units = append(units, unit{idx: i + 1, b: payload})
	}
	return rn, units, nil
}

// extendTail keeps the writer's own tail resident: an append WIDENS THE LAST
// RUN rather than starting a new one.
//
// THE REASON, RECORDED SO A MEASUREMENT CAN CONTRADICT IT: K -- the resident
// runs of one segment -- is THE ONLY TERM THE HIT PAYS, and O(log K) is what
// was approved, not O(log unbounded). A new run per append makes K grow
// without bound over a long autonomous turn, which is exactly the workload the
// window exists for and the one nobody benchmarks. Widening keeps K at 1 for
// the writer's tail; the cost is re-charging the tail's bytes on each append,
// which tree's Put already specifies (replace at an exact coord, budget
// charged the delta) and which is bounded by the tail rather than by history.
//
// IF K IS EVER MEASURED ABOVE 1 FOR A WRITER'S TAIL, THIS REASONING IS WRONG.
func (s *Segment) extendTail(payload []byte) {
	old := s.resident.Load()
	if old == nil || len(old.runs) == 0 {
		return
	}
	last := old.runs[len(old.runs)-1]
	if last.to != s.count-1 {
		return // not the tail; the append does not belong to a resident run
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	rn := run{
		from:     last.from,
		to:       last.to + 1,
		payloads: append(last.payloads[:len(last.payloads):len(last.payloads)], cp),
		bytes:    last.bytes + int64(len(cp)) + sliceHeaderBytes,
	}
	if !s.resident.CompareAndSwap(old, old.without(last.from).with(rn)) {
		return
	}
	coord := s.coordFor(rn.from, rn.to)
	regMu.Lock()
	delete(registry, s.coordFor(last.from, last.to))
	registry[coord] = s
	regMu.Unlock()
	units := make([]unit, 0, len(rn.payloads))
	for i, p := range rn.payloads {
		units = append(units, unit{idx: rn.from + uint64(i) + 1, b: p})
	}
	payloadCache.Drop(s.coordFor(last.from, last.to))
	payloadCache.Put(coord, units, false)
	if e := payloadBudget.EpochNow(); s.usedAt.Load() != e {
		s.usedAt.Store(e)
	}
}

// DropCache releases every resident range of this segment. The segment
// keeps serving reads from the file; a later read reloads the chunk it
// needs rather than the file.
func (s *Segment) DropCache() {
	old := s.resident.Swap(nil)
	if old == nil {
		return
	}
	regMu.Lock()
	for _, rn := range old.runs {
		delete(registry, s.coordFor(rn.from, rn.to))
	}
	regMu.Unlock()
	for _, rn := range old.runs {
		payloadCache.Drop(s.coordFor(rn.from, rn.to))
	}
}
