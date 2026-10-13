package tree

import (
	"testing"
	"time"
)

// An Evicted hook may take no lock, and the package honours that; the same
// hazard runs the other way if the Source is called while holding c.mu, and
// it becomes reachable the moment a re-entrant Source is installed. The call
// runs in a goroutine under a deadline so a deadlock fails rather than
// hanging the package.

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
	// No defer c.Close() here: Close takes c.mu, so on the failing path it
	// would block behind the deadlocked goroutine and turn a clean assertion
	// into a package timeout.
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
