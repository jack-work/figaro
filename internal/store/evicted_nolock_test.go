package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// EVICTED TAKES NO LOCK.
//
// forest fires Evicted outside its own locks so a lower layer can clear a
// fast pointer. The inversion runs the other way: a consumer that calls Put
// while holding its write lock can have eviction pick one of ITS runs, so
// Evicted runs with that lock held. A hook that needs the lock deadlocks, and
// only under budget pressure with concurrent readers.
//
// The first test proves the hazard is real without hanging: the hook reports
// whether the consumer's lock was already held when it fired. The second
// proves the atomic-swap escape survives the load that provokes it.

type lockingConsumer struct {
	mu   sync.Mutex
	view atomic.Pointer[[]string]

	firedUnderLock atomic.Bool
	fired          atomic.Int64
}

// evicted is the hook as it must be written: a pointer swap and nothing else.
func (c *lockingConsumer) evicted(fwtree.Coord) {
	c.fired.Add(1)
	// TryLock stands in for "would a locking hook have blocked here". It never
	// waits, so a true reading is evidence rather than a deadlock.
	if c.mu.TryLock() {
		c.mu.Unlock()
	} else {
		c.firedUnderLock.Store(true)
	}
	c.view.Store(nil)
}

func bigUnits(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(make([]byte, 4096))
	}
	return out
}

func newPressuredCache(c *lockingConsumer, budget int64) *fwtree.Cache[string] {
	cache := fwtree.New(
		func(co fwtree.Coord) ([]string, error) { return bigUnits(int(co.To - co.From)), nil },
		fwtree.NewBudget(budget),
		func(s string) int { return len(s) + 16 },
		func(s string) uint64 { return 0 },
	)
	cache.Evicted = c.evicted
	return cache
}

// The hazard is real: under budget pressure, Evicted DOES fire while the
// consumer's own write lock is held. A hook that took that lock would hang.
func TestEvictedFiresUnderTheConsumersWriteLock(t *testing.T) {
	c := &lockingConsumer{}
	cache := newPressuredCache(c, 64<<10) // small enough to evict constantly
	defer cache.Close()

	for i := 0; i < 200 && !c.firedUnderLock.Load(); i++ {
		c.mu.Lock()
		cache.Put(fwtree.Coord{Node: "n", From: uint64(i * 8), To: uint64(i*8 + 8)}, bigUnits(8), false)
		c.mu.Unlock()
	}

	if c.fired.Load() == 0 {
		t.Fatal("no eviction fired; the budget was not under pressure and the test proves nothing")
	}
	if !c.firedUnderLock.Load() {
		t.Skip("eviction never landed under the held lock in this run; the hazard is timing-dependent")
	}
}

// The escape, under the load that provokes the hazard: Put under the write
// lock, concurrent readers, a budget too small to hold the working set. A
// deadlock fails as a timeout rather than wedging the suite.
func TestPutUnderWriteLockDoesNotDeadlock(t *testing.T) {
	c := &lockingConsumer{}
	cache := newPressuredCache(c, 128<<10)
	defer cache.Close()

	lineage := []fwtree.Ref{{Node: "n", Base: 0}}
	done := make(chan struct{})

	go func() {
		defer close(done)
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					_, _ = cache.Range(lineage, 0, 64)
				}
			}()
		}
		for i := 0; i < 500; i++ {
			c.mu.Lock()
			cache.Put(fwtree.Coord{Node: "n", From: uint64(i * 8), To: uint64(i*8 + 8)}, bigUnits(8), false)
			c.mu.Unlock()
		}
		close(stop)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: Put under the consumer's write lock did not complete. " +
			"An Evicted hook must do a pointer swap and nothing else.")
	}
	if c.fired.Load() == 0 {
		t.Error("no eviction fired; the budget was not under pressure")
	}
}
