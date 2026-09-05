package tree

import (
	"fmt"
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
// The property is structural rather than decorative: it is the whole reason
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

	// Runs are cut by BYTES, so one ask can become several -- what must NOT
	// happen is a coord materialized twice or charged twice.
	runs := c.runs("p")
	var total int64
	seen := map[Coord]bool{}
	for _, r := range runs {
		if seen[r.coord] {
			t.Fatalf("coord %+v appears twice in the index", r.coord)
		}
		seen[r.coord] = true
		total += r.bytes
	}
	resident, _, _ := b.Stats()
	if resident != total {
		t.Fatalf("budget holds %d bytes for runs totalling %d: a racing miss double-charged",
			resident, total)
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

	// One RUN's worth, whatever that is in units once the byte target has cut
	// it: read the first run's span back and it must be handed over uncopied.
	if _, err := c.Range(lineage, 0, runChunk); err != nil {
		t.Fatal(err)
	}
	runs := c.runs("p")
	if len(runs) == 0 {
		t.Fatal("fixture: nothing resident")
	}
	first := runs[0]
	got, err := c.Range(lineage, first.coord.From, first.coord.To)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(first.units) {
		t.Fatalf("got %d units, the run holds %d", len(got), len(first.units))
	}
	if &got[0] != &first.units[0] {
		t.Fatal("Range copied a span that one run answers: the view contract is gone")
	}

	// And eviction under a holder does not disturb it: the run is immutable and
	// eviction publishes a successor.
	before := append([]unit(nil), got...)
	c.Drop(first.coord)
	for i := range got {
		if got[i] != before[i] {
			t.Fatalf("a holder's view changed under eviction at %d", i)
		}
	}
}

// THE ARITHMETIC GUESS MUST NOT SURVIVE A HOLE. A Source may legally return
// fewer units than its coord names, so a run's keys are not always dense; an
// unchecked index into a holed run returns A DIFFERENT UNIT'S CONTENT, which
// is a wrong answer that looks exactly like a right one.
func TestLookupIsCorrectAcrossHoles(t *testing.T) {
	// Every third key is missing: keys 1,2,4,5,7,8,...
	holed := func(c Coord) ([]unit, error) {
		var out []unit
		for k := c.From + 1; k <= c.To; k++ {
			if k%3 == 0 {
				continue
			}
			out = append(out, unit{k: k, s: fmt.Sprintf("u-%d", k)})
		}
		return out, nil
	}
	c := New(holed, NewBudget(0), func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
	defer c.Close()
	lineage := []Ref{{Node: "p"}}

	if _, err := c.Range(lineage, 0, 90); err != nil {
		t.Fatal(err)
	}
	// Every sub-span must contain exactly the keys that exist in it, in order.
	for from := uint64(0); from < 80; from += 7 {
		for _, width := range []uint64{1, 2, 5, 13} {
			to := from + width
			got, err := c.Range(lineage, from, to)
			if err != nil {
				t.Fatal(err)
			}
			var want []uint64
			for k := from + 1; k <= to; k++ {
				if k%3 != 0 {
					want = append(want, k)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("(%d..%d]: got %d units, want %d", from, to, len(got), len(want))
			}
			for i := range want {
				if got[i].k != want[i] {
					t.Fatalf("(%d..%d] index %d: got key %d (%q), want %d -- an arithmetic "+
						"index walked past a hole", from, to, i, got[i].k, got[i].s, want[i])
				}
			}
		}
	}
}
