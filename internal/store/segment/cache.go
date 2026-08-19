package segment

// The payload cache, re-seated on tree.Cache -- the ONE window shape
// this stack shares (see tree's package comment). What this file used
// to be: a 278-line singleton with its own accountant, its own epoch
// recency and its own idle sweep; each of those is tree's job now.
// What stays HERE is the property tree must not tax: the lock-free
// read path. A Segment keeps its atomic block pointer and its usedAt
// stamp; tree owns budget, eviction order (through the Recency
// oracle) and the sweep, and clears the fast pointer through the
// Evicted hook, outside every lock.

import (
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/tree"
)

type block struct {
	payloads [][]byte
	bytes    int64
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
	// and the Recency oracle. Guarded by regMu; consulted on eviction
	// and sweep (rare), never on the read fast path.
	regMu    sync.Mutex
	registry = map[tree.Coord]*Segment{}
)

func newPayloadCache() *tree.Cache[*block] {
	c := tree.New[*block](nil, payloadBudget,
		func(b *block) int { return int(b.bytes) },
		func(*block) uint64 { return 1 })
	c.Evicted = func(coord tree.Coord) {
		regMu.Lock()
		s := registry[coord]
		delete(registry, coord)
		regMu.Unlock()
		if s != nil {
			s.block.Store(nil)
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

// coordOf names a segment's run: one run per segment, keyed by the
// identity the file already has.
func (s *Segment) coordOf() tree.Coord {
	return tree.Coord{Node: s.path, From: 0, To: 1}
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
		// Disabling drops everything held; charge(0) triggers no pass,
		// so sweep everything regardless of age.
		payloadBudget.TrimIdle(-1)
	}
}

// CacheBudget reports the configured bound.
func CacheBudget() int64 { _, limit, _ := payloadBudget.Stats(); return limit }

// CachedBytes reports the payload bytes currently resident.
func CachedBytes() int64 { resident, _, _ := payloadBudget.Stats(); return resident }

// CacheLoads reports whole-segment loads to date: the thrash alarm.
// Climbing with READS rather than with distinct segments means blocks
// are being dropped as fast as they are built.
func CacheLoads() int64 { return loads.Load() }

// CachedSegments reports how many segments hold a resident block.
func CachedSegments() int {
	regMu.Lock()
	defer regMu.Unlock()
	return len(registry)
}

// cachedPayloads returns this segment's block, loading it if the budget
// allows. A nil result means the caller must read from the file. The
// fast path is one atomic load and, at most, one stale-epoch store --
// tree is not consulted at all.
func (s *Segment) cachedPayloads() *block {
	if b := s.block.Load(); b != nil {
		if e := payloadBudget.EpochNow(); s.usedAt.Load() != e {
			s.usedAt.Store(e)
		}
		return b
	}
	if CacheBudget() <= 0 {
		return nil
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if b := s.block.Load(); b != nil {
		return b
	}
	b, err := s.readAllPayloads()
	if err != nil {
		return nil
	}
	loads.Add(1)
	if !s.block.CompareAndSwap(nil, b) {
		return s.block.Load()
	}
	coord := s.coordOf()
	regMu.Lock()
	registry[coord] = s
	regMu.Unlock()
	s.usedAt.Store(payloadBudget.EpochNow())
	payloadCache.Put(coord, []*block{b}, false)
	return b
}

// SweepIdle drops every block not read since `keep` sweeps ago and
// advances the epoch: tree.TrimIdle with the segments' own usedAt as
// the recency oracle. A caller with an idle clock of its own should
// drive this from the same sweep rather than invent another number.
func SweepIdle(keep int64) (dropped int, freed int64) {
	if keep < 0 {
		return 0, 0
	}
	return payloadBudget.TrimIdle(keep)
}

// readAllPayloads reads every record of the segment in one pass.
func (s *Segment) readAllPayloads() (*block, error) {
	b := &block{payloads: make([][]byte, 0, s.count)}
	for i := uint64(0); i < s.count; i++ {
		off := s.offsets[i]
		nextOff := s.size
		if int(i)+1 < len(s.offsets) {
			nextOff = s.offsets[i+1]
		}
		payload, _, err := s.codec.ReadFrame(s.f, off, nextOff)
		if err != nil {
			return nil, err
		}
		b.payloads = append(b.payloads, payload)
		b.bytes += int64(len(payload)) + sliceHeaderBytes
	}
	return b, nil
}

// extendBlock keeps the writer's own tail resident: when the active
// segment already has a block, an append lands in it instead of
// invalidating it. The CAS keeps the accounting honest against a
// concurrent eviction; the re-Put replaces the run and charges the
// delta.
func (s *Segment) extendBlock(payload []byte) {
	old := s.block.Load()
	if old == nil {
		return
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	nb := &block{
		payloads: append(old.payloads[:len(old.payloads):len(old.payloads)], cp),
		bytes:    old.bytes + int64(len(cp)) + sliceHeaderBytes,
	}
	if s.block.CompareAndSwap(old, nb) {
		payloadCache.Put(s.coordOf(), []*block{nb}, false)
		if e := payloadBudget.EpochNow(); s.usedAt.Load() != e {
			s.usedAt.Store(e)
		}
	}
}

// DropCache releases this segment's payload block. The segment keeps
// serving reads from the file; a later read reloads it.
func (s *Segment) DropCache() {
	coord := s.coordOf()
	regMu.Lock()
	delete(registry, coord)
	regMu.Unlock()
	s.block.Store(nil)
	payloadCache.Drop(coord)
}
