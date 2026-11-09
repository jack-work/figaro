// Package tree is the one shape every derived cache in this stack shares: a
// window of materialized units over a durable substrate.
package tree

import (
	"sync"
	"sync/atomic"
	"time"
)

// Ref is one step of a lineage: a node and the coordinate at which its child
// diverged (the fork base).
type Ref struct {
	Node string
	Base uint64 // first coordinate that is the CHILD's own; 0 for the root
}

// Coord names a contiguous unit range within one node, (From..To].
type Coord struct {
	Node     string
	From, To uint64
}

// Source rematerializes the units of a coord from the layer below. Returning
// fewer units than the coord names is legal: a hole degrades to a gap, never
// a lie.
type Source[U any] func(Coord) ([]U, error)

// Sizer estimates one unit's resident bytes at insert; count the strings that
// dominate rather than reflect-walking.
type Sizer[U any] func(U) int

// Keyer names the coordinate of one unit, so a run's index can be
// rebuilt from its units and a Range can slice exactly.
type Keyer[U any] func(U) uint64

// Budget is the shared byte bound across every Cache that holds it: one per
// concern.
type Budget struct {
	limit atomic.Int64
	bytes atomic.Int64
	epoch atomic.Int64

	// pressure says the budget is over its limit. charge raises it; the
	// daemon's standing sweep lowers it.
	pressure atomic.Bool

	// owners is published whole: charge and TrimIdle read it on the eviction
	// path, where a mutex would serialize every cache sharing this budget.
	ownersMu sync.Mutex // writers only
	owners   atomic.Pointer[[]owner]

	evictions atomic.Int64
}

type owner interface {
	// coldest reports the owner's least-recent evictable run's epoch,
	// and whether one exists.
	coldest() (int64, bool)
	// evictColdest hollows that run and returns the bytes freed.
	evictColdest() int64
	// evictOlderThan hollows EVERY evictable run older than cutoff, in one
	// pass. The idle sweep knows its predicate before it starts, so it does
	// not have to ask "who is coldest" once per victim.
	evictOlderThan(cutoff int64) (dropped int, freed int64)
}

// NewBudget bounds its caches to limit bytes; 0 is unbounded.
func NewBudget(limit int64) *Budget {
	b := &Budget{}
	b.owners.Store(&[]owner{})
	b.limit.Store(limit)
	return b
}

// SetLimit retunes the bound live (the config reload path).
func (b *Budget) SetLimit(limit int64) { b.limit.Store(limit) }

// Stats reports resident bytes, the limit, and evictions to date.
func (b *Budget) Stats() (resident, limit, evictions int64) {
	if b == nil {
		return 0, 0, 0
	}
	return b.bytes.Load(), b.limit.Load(), b.evictions.Load()
}

// charge accounts delta and raises pressure. It never evicts on the caller's
// goroutine and starts nothing of its own: the daemon's standing sweep calls
// Sweep on its own beat, so the overshoot is bounded by the reaper's cadence,
// not by a second limit.
func (b *Budget) charge(delta int64) {
	if b == nil {
		return
	}
	b.bytes.Add(delta)
	if b.limit.Load() <= 0 {
		return
	}
	if b.bytes.Load() > b.limit.Load() {
		b.pressure.Store(true)
	}
}

// Sweep evicts the globally coldest run until the total fits, and reports what
// it freed. It is the ONLY place eviction happens under pressure. Cheap when
// there is none: one atomic read.
func (b *Budget) Sweep() (dropped int, freed int64) {
	if b == nil || !b.pressure.Load() {
		return 0, 0
	}
	for {
		limit := b.limit.Load()
		if limit <= 0 || b.bytes.Load() <= limit {
			b.pressure.Store(false)
			return dropped, freed
		}
		var victim owner
		var coldestEpoch int64
		for _, o := range *b.owners.Load() {
			if e, ok := o.coldest(); ok && (victim == nil || e < coldestEpoch) {
				victim, coldestEpoch = o, e
			}
		}
		if victim == nil {
			b.pressure.Store(false)
			return dropped, freed // only pins remain; the meter still tells the truth
		}
		n := victim.evictColdest()
		if n <= 0 {
			return dropped, freed // victim raced away; the next charge raises pressure again
		}
		b.bytes.Add(-n)
		b.evictions.Add(1)
		dropped++
		freed += n
	}
}

// Settle sweeps until the budget fits or the deadline passes. FOR TESTS AND
// doctor: production never waits on eviction.
func (b *Budget) Settle(within time.Duration) bool {
	if b == nil {
		return true
	}
	deadline := time.Now().Add(within)
	for {
		b.Sweep()
		if limit := b.limit.Load(); limit <= 0 || b.bytes.Load() <= limit {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// epochNow reads the current epoch without advancing it: touches load,
// loads and sweeps advance.
func (b *Budget) EpochNow() int64 {
	if b == nil {
		return 0
	}
	return b.epoch.Load()
}

// TrimIdle evicts every unpinned run older than the current epoch minus keep,
// then advances the epoch. One pass per owner: the cutoff is fixed before the
// sweep starts, so the loop never asks "who is coldest" per victim.
func (b *Budget) TrimIdle(keep int64) (dropped int, freed int64) {
	if b == nil {
		return 0, 0
	}
	cutoff := b.epoch.Add(1) - 1 - keep
	for _, o := range *b.owners.Load() {
		d, f := o.evictOlderThan(cutoff)
		if d == 0 {
			continue
		}
		b.bytes.Add(-f)
		b.evictions.Add(int64(d))
		dropped += d
		freed += f
	}
	return dropped, freed
}

func (b *Budget) adopt(o owner) {
	if b == nil {
		return
	}
	b.ownersMu.Lock()
	cur := *b.owners.Load()
	next := make([]owner, len(cur), len(cur)+1)
	copy(next, cur)
	next = append(next, o)
	b.owners.Store(&next)
	b.ownersMu.Unlock()
}

func (b *Budget) disown(o owner) {
	if b == nil {
		return
	}
	b.ownersMu.Lock()
	cur := *b.owners.Load()
	next := make([]owner, 0, len(cur))
	for _, x := range cur {
		if x != o {
			next = append(next, x)
		}
	}
	b.owners.Store(&next)
	b.ownersMu.Unlock()
}
