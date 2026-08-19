package tree

import (
	"sync"
	"testing"
	"time"
)

// A HIT TAKES NO LOCK, asserted the only way that cannot be argued with: hold
// c.mu -- the writers' lock -- and serve a warm range from another goroutine.
// If the read path touched the lock at all, this deadlocks and the test times
// out. A benchmark can only say the read got faster; this says WHY, and it
// goes red the moment someone reintroduces a lock on the read path for a
// reason that looks good locally.
//
// The property is load-bearing rather than decorative: it is the whole reason
// one uniform window can own the hot tail as well as the cold ranges, and a
// mutex here is what kept a second, flat cache shape alive beside this one
// (plans/log-cache-policy.md).
func TestHitTakesNoLock(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newCache(NewBudget(0), &calls, &mu)
	lineage := []Ref{{Node: "p"}}

	if u, err := c.Range(lineage, 0, 64); err != nil || len(u) != 64 {
		t.Fatalf("warm: %d units err=%v", len(u), err)
	}
	warmCalls := calls

	c.mu.Lock()
	defer c.mu.Unlock()

	done := make(chan int, 1)
	go func() {
		u, err := c.Range(lineage, 0, 64)
		if err != nil {
			done <- -1
			return
		}
		done <- len(u)
	}()

	select {
	case n := <-done:
		if n != 64 {
			t.Fatalf("warm read under a held writer lock returned %d units", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a warm read blocked on the writers' lock: the read path is not lock-free")
	}

	if calls != warmCalls {
		t.Fatalf("the warm read called the source %d times; it must hit", calls-warmCalls)
	}
}

// A MISS STILL PUBLISHES ONE RUN, whoever wins. Two callers racing the same
// cold coord may both call the Source -- that trade is deliberate, one wasted
// read against a lock held across I/O -- but exactly one run may end up in the
// index, and the budget may be charged exactly once for it.
func TestRacingMissesPublishOneRun(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(0)
	c := newCache(b, &calls, &mu)
	lineage := []Ref{{Node: "p"}}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if u, err := c.Range(lineage, 0, 64); err != nil || len(u) != 64 {
				t.Errorf("racing read: %d units err=%v", len(u), err)
			}
		}()
	}
	wg.Wait()

	runs := c.runs("p")
	if len(runs) != 1 {
		t.Fatalf("want one run in the index, got %d", len(runs))
	}
	resident, _, _ := b.Stats()
	if resident != runs[0].bytes {
		t.Fatalf("budget holds %d bytes for a run of %d: a racing miss double-charged",
			resident, runs[0].bytes)
	}
}

// Registering and forgetting caches must not lose a sibling: the budget's
// owner list is published whole, and a successor built from a stale copy would
// silently stop charging one cache -- a leak that reads as "the meter says
// zero at peak retention", which this package's own package comment calls the
// worst possible meter.
func TestBudgetOwnersSurviveConcurrentRegistration(t *testing.T) {
	b := NewBudget(0)
	var calls int
	var mu sync.Mutex

	const n = 16
	caches := make([]*Cache[unit], n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			caches[i] = newCache(b, &calls, &mu)
		}(i)
	}
	wg.Wait()

	if got := len(*b.owners.Load()); got != n {
		t.Fatalf("registered %d caches, the budget holds %d", n, got)
	}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); caches[i].Close() }(i)
	}
	wg.Wait()

	if got := len(*b.owners.Load()); got != 0 {
		t.Fatalf("after closing every cache the budget still holds %d owners", got)
	}
}

// RANGE HANDS BACK A VIEW, NOT A COPY, when one run answers the whole span.
// Pinned as pointer identity because it is a CONTRACT, not an optimisation: a
// caller that mutates what it is given corrupts the cache, and a future
// "defensive copy" would silently undo the fix that took the hot-tail read
// from 4.24x the flat window's to 1.27x.
func TestRangeOfOneRunAliasesTheCache(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newCache(NewBudget(0), &calls, &mu)
	lineage := []Ref{{Node: "p"}}

	// One chunk, so one run answers.
	if _, err := c.Range(lineage, 0, runChunk); err != nil {
		t.Fatal(err)
	}
	runs := c.runs("p")
	if len(runs) != 1 {
		t.Fatalf("fixture: %d runs, want 1", len(runs))
	}

	got, err := c.Range(lineage, 0, runChunk)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != runChunk {
		t.Fatalf("got %d units", len(got))
	}
	if &got[0] != &runs[0].units[0] {
		t.Fatal("Range copied a span that one run answers: the view contract is gone")
	}

	// And eviction under a holder does not disturb it: the run is immutable and
	// eviction publishes a successor.
	before := append([]unit(nil), got...)
	c.Drop(runs[0].coord)
	for i := range got {
		if got[i] != before[i] {
			t.Fatalf("a holder's view changed under eviction at %d", i)
		}
	}
}
