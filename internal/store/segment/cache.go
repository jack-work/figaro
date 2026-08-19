package segment

// The payload cache. THE RUNS LIVE IN tree AND NOWHERE ELSE.
//
// This file used to carry a SECOND INDEX of the same data beside tree's: an
// immutable `residency` of `run`s behind an atomic pointer, with find/with/
// without, a coord->Segment registry and an Evicted hook to keep the two in
// step. Its stated reason is in this file's history: "Routing this through
// tree.Range would put a process-wide mutex on the hottest path in the read
// stack; it is not done, it was not approved."
//
// THAT PREMISE DIED AT 63902f44. tree's reads take no lock: every structure a
// reader touches is immutable once published, and writers hold c.mu only to
// publish -- never across the Source, the budget's eviction pass, or the
// Evicted hook. So the duplicate has no reason left, and a duplicate kept past
// its reason is the accretion this consolidation exists to remove.
//
// WHAT WENT WITH IT: `residency`, `run`, find/with/without, the CAS dance in
// extendTail, the Evicted hook, the Recency oracle and the per-segment usedAt
// stamp. tree already tracks a run's recency on its own read path, so the
// oracle was telling it something it now knows.
//
// WHAT REMAINS HERE IS THE ONE THING TREE CANNOT DO: turn a coord back into
// the file that answers it. `registry` is now node-path -> *Segment, consulted
// by the SOURCE on a miss, and it exists because tree names nodes by string
// and cannot open a segment.

import (
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/tree"
)

// chunkRecords bounds a materialized range. A miss reads the ALIGNED chunk
// containing the record asked for, so a sequential scan materializes each
// chunk exactly once and never re-reads a record -- the traversal the fig IR
// encode path performs on every turn.
const chunkRecords = 32

// unit is one record's payload with the coordinate tree slices by. A bare
// []byte cannot answer the Keyer.
type unit struct {
	idx uint64 // 1-based: record i has coordinate i+1, matching (From..To]
	b   []byte
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

	// registry maps a node path to the segment that can read it, for the
	// SOURCE. Consulted on a MISS only; the hit path never touches it.
	regMu    sync.Mutex
	registry = map[string]*Segment{}
)

func segmentFor(path string) *Segment {
	regMu.Lock()
	defer regMu.Unlock()
	return registry[path]
}

func newPayloadCache() *tree.Cache[unit] {
	return tree.New[unit](
		// THE SOURCE. tree could not rematerialize anything before this: the
		// cache was built with src = nil, so Range, per-gap refill and
		// hollow-keeps-the-index were UNREACHABLE from this tenant. Installing
		// it is what lets the runs live there at all.
		func(coord tree.Coord) ([]unit, error) {
			s := segmentFor(coord.Node)
			if s == nil {
				return nil, nil // a miss, never a lie: the segment is gone
			}
			loads.Add(1)
			return s.readRange(coord.From, coord.To)
		},
		payloadBudget,
		func(u unit) int { return len(u.b) + sliceHeaderBytes },
		func(u unit) uint64 { return u.idx })
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
// THE UNIT OF THIS NUMBER IS A RANGE, NOT A SEGMENT. It is surfaced as
// segment_cache_loads (rpc) and printed by doctor, and it is NOT comparable
// with values recorded before the cache unit became a range.
func CacheLoads() int64 { return loads.Load() }

// CachedRanges reports how many resident ranges tree holds for segments.
func CachedRanges() int { return payloadCache.ResidentRuns() }

// cachedPayload returns record i's payload if the cache can serve it.
//
// THE HIT TAKES NO LOCK, and it takes no lock because TREE's read path takes
// none -- not because this package keeps its own copy of the index. RangeAt
// rather than Range: a segment file has no lineage (forks live at disk.Log,
// which delegates below a fork base to the parent LOG), so building a
// one-element []Ref and walking split() on every hit would be two allocations
// to arrive at a coord already known.
func (s *Segment) cachedPayload(i uint64) ([]byte, bool) {
	if CacheBudget() <= 0 {
		return nil, false
	}
	// THE HIT IS A POINT LOOKUP, NOT A RANGE READ. Asking the range surface
	// for one record allocated and copied the whole chunk to answer -- 1153
	// B/op against 3, measured -- so the hit takes tree.At and only a MISS
	// widens to a chunk.
	if u, ok := s.handle().At(i + 1); ok {
		return u.b, true
	}
	s.register()
	// MISS: the ALIGNED chunk containing i, so a sequential scan materializes
	// each chunk exactly once.
	lo := (i / chunkRecords) * chunkRecords
	hi := lo + chunkRecords
	if hi > s.count {
		hi = s.count
	}
	if _, err := s.handle().Range(lo, hi); err != nil {
		return nil, false
	}
	if u, ok := s.handle().At(i + 1); ok {
		return u.b, true
	}
	return nil, false
}

// handle resolves this segment's node in the payload cache ONCE and keeps it.
//
// The name is a filesystem path and every string-keyed accessor hashes it. A
// read path that touches one node per record must not pay a hash per record --
// measured as the whole of a 2.2x serial regression once allocation was gone.
// Naming happens at open.
func (s *Segment) handle() *tree.Handle[unit] {
	if h := s.h.Load(); h != nil {
		return h
	}
	h := payloadCache.Node(s.path)
	if !s.h.CompareAndSwap(nil, h) {
		return s.h.Load()
	}
	return h
}

// register makes this segment findable by the Source. Idempotent and off the
// hot path's critical work: it is a map write per read, which is why it is
// guarded by a CAS on a flag rather than taking regMu every time.
func (s *Segment) register() {
	if s.registered.Load() {
		return
	}
	regMu.Lock()
	registry[s.path] = s
	regMu.Unlock()
	s.registered.Store(true)
}

// SweepIdle drops every run not read since `keep` sweeps ago and advances the
// epoch.
func SweepIdle(keep int64) (dropped int, freed int64) {
	if keep < 0 {
		return 0, 0
	}
	return payloadBudget.TrimIdle(keep)
}

// readRange reads records [from, to) and NOTHING ELSE: O(records in range)
// where the pre-range cache read O(records in segment).
func (s *Segment) readRange(from, to uint64) ([]unit, error) {
	if to > s.count {
		to = s.count
	}
	units := make([]unit, 0, to-from)
	for i := from; i < to; i++ {
		off := s.offsets[i]
		nextOff := s.size
		if int(i)+1 < len(s.offsets) {
			nextOff = s.offsets[i+1]
		}
		payload, _, err := s.codec.ReadFrame(s.f, off, nextOff)
		if err != nil {
			return nil, err
		}
		units = append(units, unit{idx: i + 1, b: payload})
	}
	return units, nil
}

// extendTail keeps the writer's own tail resident. It is now a tree.Put at the
// tail coord, which tree documents as replace-in-place with the budget charged
// the delta -- the CAS dance this file used to run against its own pointer is
// gone with the pointer.
//
// APPEND WIDENS THE LAST RUN rather than starting a new one, and the reason is
// unchanged: the number of runs is the only term a lookup pays, and a new run
// per append grows it without bound over a long autonomous turn -- the exact
// workload the window exists for. IF THAT NUMBER IS EVER MEASURED GROWING FOR
// A WRITER'S TAIL, THIS REASONING IS WRONG.
func (s *Segment) extendTail(payload []byte) {
	if CacheBudget() <= 0 {
		return
	}
	last := s.count - 1 // the record just appended, 0-based
	lo := (last / chunkRecords) * chunkRecords
	// ResidentAt, NOT RangeAt: an append must EXTEND what is resident and must
	// never FAULT IT IN. Materializing here re-creates residency the evictor
	// just dropped, and an append loop racing a sweep livelocks -- each undoes
	// the other. Measured: it hung TestCacheAccountingSurvivesAppendVersusEvict
	// for 25 seconds before this line said ResidentAt.
	units, ok := s.handle().ResidentAt(lo, last)
	if !ok {
		return // the tail chunk is not resident; nothing to extend
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	s.handle().Put(lo, last+1, append(units, unit{idx: last + 1, b: cp}), false)
}

// DropCache releases every resident range of this segment. The segment keeps
// serving reads from the file; a later read reloads the chunk it needs.
func (s *Segment) DropCache() {
	s.handle().Drop()
	regMu.Lock()
	delete(registry, s.path)
	regMu.Unlock()
	s.registered.Store(false)
}
