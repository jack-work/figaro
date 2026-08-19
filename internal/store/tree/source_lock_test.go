package tree

import (
	"testing"
	"time"
)

// THE PACKAGE FOLLOWS ITS OWN RULE IN ONE DIRECTION ONLY.
//
// docs/store/tree.md requires that an Evicted hook take NO lock: eviction can
// fire while a consumer holds its own write lock, so a hook that needs that
// lock deadlocks -- "and only under budget pressure with concurrent readers,
// which is the shape that reaches production first". The rule is honoured for
// Evicted, which is invoked outside every lock.
//
// materializeLocked CALLS A CALLER-SUPPLIED Source WHILE HOLDING c.mu. Same
// hazard, opposite direction, in the package that states the rule.
//
// WHY IT IS NOT LIVE TODAY, AND WHY THAT IS THE REASON TO FIX IT NOW. The only
// tenant (segment's payload cache) passes src = nil, so no caller-supplied
// code runs under c.mu and the deadlock is UNREACHABLE. IT BECOMES REACHABLE
// THE MOMENT A Source IS INSTALLED -- which is precisely what the
// consolidation does, since a tree cache that can rematerialize is the whole
// point of moving figaro's logs onto it. A hazard with a known arrival date.
//
// THIS TEST FAILS RATHER THAN HANGS. A deadlock in a test blocks the package
// until go test's global timeout and reports as a panic in an unrelated place,
// so the call runs in a goroutine under a deadline and the assertion is that
// it FINISHED. The goroutine is leaked when it does not; that is the honest
// cost of demonstrating a deadlock and it is bounded to a failing run.

// reentrantSource calls back into the cache it is serving, which is the
// smallest faithful model of a real Source: a layer below that consults the
// same window (a fork reading its parent's prefix, a composed layer asking the
// decoded one) reaches this shape without anybody intending it.
func reentrantSource(c *Cache[string], reentered *bool) Source[string] {
	return func(coord Coord) ([]string, error) {
		// THE RE-ENTRY. Under the defect this blocks forever on c.mu, which
		// the caller is holding while waiting for this function to return.
		_ = c.Recomposes()
		*reentered = true
		out := make([]string, 0, coord.To-coord.From)
		for i := coord.From + 1; i <= coord.To; i++ {
			out = append(out, "u")
		}
		return out, nil
	}
}

func TestASourceMustNotRunUnderTheCacheLock(t *testing.T) {
	var c *Cache[string]
	reentered := false
	c = New[string](func(coord Coord) ([]string, error) {
		return reentrantSource(c, &reentered)(coord)
	}, NewBudget(1<<20),
		func(string) int { return 8 },
		func(string) uint64 { return 1 })
	// NO defer c.Close() HERE, DELIBERATELY. Close takes c.mu, so on the
	// failing path it would block behind the deadlocked goroutine and turn a
	// clean 3-second assertion into a 60-second package timeout reported as a
	// panic somewhere else. My first version did exactly that.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Range([]Ref{{Node: "n", Base: 0}}, 0, 4)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("DEADLOCK: a Source that touches the cache never returned. " +
			"materializeLocked invoked it while holding c.mu, which is the exact " +
			"inversion docs/store/tree.md forbids for the Evicted hook -- the rule " +
			"applied in one direction and not the other. (A goroutine is leaked by " +
			"this failure; that is the cost of demonstrating a deadlock.)")
	}
	if !reentered {
		t.Fatal("the Source never re-entered the cache; this test cannot witness " +
			"the hazard it is written for")
	}
}

// AND THE PROPERTY THE FIX MUST NOT BREAK: a miss still materializes, is still
// charged, and is still resident afterwards. Releasing the lock around the
// Source must not lose the run.
func TestAMissStillBecomesResidentWhenTheSourceRunsUnlocked(t *testing.T) {
	calls := 0
	c := New[string](func(coord Coord) ([]string, error) {
		calls++
		out := make([]string, 0, coord.To-coord.From)
		for i := coord.From + 1; i <= coord.To; i++ {
			out = append(out, "u")
		}
		return out, nil
	}, NewBudget(1<<20),
		func(string) int { return 8 },
		func(string) uint64 { return 1 })
	defer c.Close()

	lineage := []Ref{{Node: "n", Base: 0}}
	got, err := c.Range(lineage, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("first read returned %d units, want 4", len(got))
	}
	first := calls

	got, err = c.Range(lineage, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("second read returned %d units, want 4", len(got))
	}
	if calls != first {
		t.Fatalf("the second read called the Source again (%d -> %d): the "+
			"materialized run was not installed", first, calls)
	}
}
