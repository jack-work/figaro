// Package tree is the ONE shape every derived cache in this stack
// shares: a window of materialized units over a durable substrate,
package tree

import (
	"sync"
	"sync/atomic"
)

// Ref is one step of a lineage: a trunk node and the coordinate at
// which its child diverged (the fork base). A read below the base
type Ref struct {
	Node string
	Base uint64 // first coordinate that is the CHILD's own; 0 for the root
}

// Coord names a contiguous unit range within ONE node, (From..To]
// inclusive of To, exclusive of From -- the bracket convention every
// substrate in this stack already speaks.
type Coord struct {
	Node     string
	From, To uint64
}

// Source rematerializes the units of a coord from the layer below.
// Returning fewer units than the coord names is legal (a hole degrades
// to a gap, never a lie); returning an error poisons nothing -- the
type Source[U any] func(Coord) ([]U, error)

// Sizer estimates one unit's resident bytes. An estimate at insert,
// like every window in this stack; do not reflect-walk (that lied 3x
// low once already) -- count the strings that dominate.
type Sizer[U any] func(U) int

// Keyer names the coordinate of one unit, so a run's index can be
// rebuilt from its units and a Range can slice exactly.
type Keyer[U any] func(U) uint64

// ---- the accountant ----

// Budget is the shared byte bound across every Cache that holds it.
// One per concern (raw / decoded / composed today; one pool tomorrow is
// a config choice, not a rewrite). It never calls an owner while
type Budget struct {
	limit atomic.Int64
	bytes atomic.Int64
	epoch atomic.Int64

	// owners is PUBLISHED WHOLE: charge and TrimIdle read it on the eviction
	// path, where a mutex would serialize every cache that shares this budget
	ownersMu sync.Mutex // WRITERS ONLY
	owners   atomic.Pointer[[]owner]

	evictions atomic.Int64
}

type owner interface {
	// coldest reports the owner's least-recent evictable run's epoch,
	// and whether one exists.
	coldest() (int64, bool)
	// evictColdest hollows that run and returns the bytes freed.
	evictColdest() int64
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

// Stats reports resident bytes, the limit, and evictions to date --
// every resident structure arrives with its number in doctor mem.
func (b *Budget) Stats() (resident, limit, evictions int64) {
	if b == nil {
		return 0, 0, 0
	}
	return b.bytes.Load(), b.limit.Load(), b.evictions.Load()
}

// charge admits delta bytes and evicts the globally coldest runs until
// the budget fits. Called by caches on insert and re-tally.
func (b *Budget) charge(delta int64) {
	if b == nil {
		return
	}
	b.bytes.Add(delta)
	limit := b.limit.Load()
	if limit <= 0 {
		return
	}
	for b.bytes.Load() > limit {
		var victim owner
		var coldestEpoch int64
		for _, o := range *b.owners.Load() {
			if e, ok := o.coldest(); ok && (victim == nil || e < coldestEpoch) {
				victim, coldestEpoch = o, e
			}
		}
		if victim == nil {
			return // only pins remain; the meter still tells the truth
		}
		if freed := victim.evictColdest(); freed > 0 {
			b.bytes.Add(-freed)
			b.evictions.Add(1)
		} else {
			return // victim raced away; next charge retries
		}
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

// TrimIdle evicts every unpinned run whose epoch is older than the
// current epoch minus keep, then advances the epoch: the idle sweep,
// generalized. Returns runs dropped and bytes freed.
func (b *Budget) TrimIdle(keep int64) (dropped int, freed int64) {
	if b == nil {
		return 0, 0
	}
	cutoff := b.epoch.Add(1) - 1 - keep
	for _, o := range *b.owners.Load() {
		for {
			e, ok := o.coldest()
			if !ok || e >= cutoff {
				break
			}
			f := o.evictColdest()
			if f <= 0 {
				break
			}
			b.bytes.Add(-f)
			b.evictions.Add(1)
			dropped++
			freed += f
		}
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
