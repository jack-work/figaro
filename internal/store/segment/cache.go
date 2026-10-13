package segment

// The payload cache; the runs live in tree and nowhere else.

import (
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/tree"
)

// chunkRecords bounds a materialized range: a miss reads the aligned chunk
// containing the record, so a sequential scan materializes each chunk once.
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

const defaultCacheBudget = 32 << 20

var (
	payloadBudget = tree.NewBudget(defaultCacheBudget)
	payloadCache  = newPayloadCache()

	loads atomic.Int64

	// registry maps a node path to the segment that can read it; consulted
	// on a miss only.
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
		// The Source: without it tree could rematerialize nothing.
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

// CacheLoads counts range materializations: climbing with reads rather than
// with distinct ranges means runs are dropped as fast as they are built.
func CacheLoads() int64 { return loads.Load() }

// CachedRanges reports how many resident ranges tree holds for segments.
func CachedRanges() int { return payloadCache.ResidentRuns() }

// cachedPayload returns record i's payload if the cache can serve it.
func (s *Segment) cachedPayload(i uint64) ([]byte, bool) {
	if CacheBudget() <= 0 {
		return nil, false
	}
	// A point lookup, not a range read: asking the range surface for one
	// record copied the whole chunk.
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

// register is idempotent and guarded by a CAS on a flag rather than taking
// regMu on every read.
func (s *Segment) register() {
	if s.registered.Load() {
		return
	}
	regMu.Lock()
	registry[s.path] = s
	regMu.Unlock()
	s.registered.Store(true)
}

// SweepBudget is the segment cache's half of the standing sweep: charge
// raises pressure and the daemon's sweep lowers it.
func SweepBudget() (dropped int, freed int64) { return payloadBudget.Sweep() }

// SweepIdle drops every run not read since `keep` sweeps ago and advances the
// epoch.
func SweepIdle(keep int64) (dropped int, freed int64) {
	if keep < 0 {
		return 0, 0
	}
	return payloadBudget.TrimIdle(keep)
}

// readRange reads records [from, to) and nothing else.
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

// extendTail keeps the writer's own tail resident via a Put at the tail coord.
func (s *Segment) extendTail(payload []byte) {
	if CacheBudget() <= 0 {
		return
	}
	last := s.count - 1 // the record just appended, 0-based
	lo := (last / chunkRecords) * chunkRecords
	// ResidentAt, not RangeAt: an append must extend what is resident and
	// never fault it in.
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
