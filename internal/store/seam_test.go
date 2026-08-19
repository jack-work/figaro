package store

import (
	"fmt"
	"sync"
	"testing"
)

// THE SEAM: every read path forks between the resident tail and whatever
// answers below it -- c.inner today, a frozen forest run after the re-seat.
// Two structures that can answer for the same LT is the hazard, and a
// duplicate reads as a real record rather than an error: a translation would
// encode the same message twice and the per-LT cache would make it permanent.
// Not a crash, a lie that survives -- the displaced-tool_result family.
//
// Asserted on the LT SEQUENCE, never on contents: contents can coincide,
// coordinates cannot. Green today, so a re-seat that moves the boundary
// without moving the ownership fails here.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING. This invariant predates the forest
// re-seat and outlives it: do not retire this file with the temporary code.

func seamLog(t *testing.T, n int) *cachedLog[string] {
	t.Helper()
	inner := NewMemLog[string]()
	for i := 1; i <= n; i++ {
		if _, err := inner.Append(Entry[string]{Payload: fmt.Sprintf("row-%04d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return newWindowedLog[string](inner, 0, 0, 1, 1, func(e Entry[string]) int { return len(e.Payload) + 48 })
}

// exactlyOnceInOrder is the whole assertion: strictly ascending LTs, so a
// duplicate and a reordering both fail, and neither can hide in the contents.
func exactlyOnceInOrder(t *testing.T, what string, got []Entry[string]) {
	t.Helper()
	seen := make(map[uint64]int, len(got))
	for i, e := range got {
		if prev, dup := seen[e.FigaroLT]; dup {
			t.Fatalf("%s: LT %d returned twice (positions %d and %d)", what, e.FigaroLT, prev, i)
		}
		seen[e.FigaroLT] = i
		if i > 0 && e.FigaroLT <= got[i-1].FigaroLT {
			t.Fatalf("%s: LT out of order at %d: %d after %d", what, i, e.FigaroLT, got[i-1].FigaroLT)
		}
	}
}

// contiguous also rejects a GAP, which is the mirror error: an off-by-one that
// drops the boundary LT instead of duplicating it.
func contiguous(t *testing.T, what string, got []Entry[string]) {
	t.Helper()
	for i := 1; i < len(got); i++ {
		if got[i].FigaroLT != got[i-1].FigaroLT+1 {
			t.Fatalf("%s: gap at %d: %d follows %d", what, i, got[i].FigaroLT, got[i-1].FigaroLT)
		}
	}
}

func TestReadsSpanningTheSeamReturnEachLTExactlyOnce(t *testing.T) {
	const total = 400
	c := seamLog(t, total)
	if dropped := c.Trim(100); dropped == 0 {
		t.Fatal("nothing trimmed; there is no seam and the test proves nothing")
	}
	v := c.load()
	if v.trimmed == 0 || len(v.rows) == 0 {
		t.Fatal("no seam: the window holds everything or nothing")
	}
	boundary := v.rows[0].FigaroLT // first resident LT; below it is the other side

	t.Run("Read", func(t *testing.T) {
		got := c.Read()
		exactlyOnceInOrder(t, "Read", got)
		contiguous(t, "Read", got)
		if len(got) != total {
			t.Fatalf("Read returned %d of %d", len(got), total)
		}
	})

	t.Run("ReadFrom across", func(t *testing.T) {
		got := c.ReadFrom(boundary-10, 0)
		exactlyOnceInOrder(t, "ReadFrom", got)
		contiguous(t, "ReadFrom", got)
		if len(got) == 0 || got[0].FigaroLT != boundary-10 {
			t.Fatalf("ReadFrom(%d) started at %v", boundary-10, got[0].FigaroLT)
		}
	})

	t.Run("ReadPage across", func(t *testing.T) {
		got, _ := c.ReadPage(boundary-10, 0, 40)
		exactlyOnceInOrder(t, "ReadPage", got)
		contiguous(t, "ReadPage", got)
	})

	t.Run("TailAfter across", func(t *testing.T) {
		got, _ := c.TailAfter(uint64(boundary - 10))
		exactlyOnceInOrder(t, "TailAfter", got)
		contiguous(t, "TailAfter", got)
	})

	t.Run("exactly at the boundary", func(t *testing.T) {
		// The boundary LT itself must be answered by exactly one side.
		got := c.ReadFrom(boundary, 0)
		exactlyOnceInOrder(t, "ReadFrom(boundary)", got)
		if len(got) == 0 || got[0].FigaroLT != boundary {
			t.Fatalf("boundary LT %d not returned first: %v", boundary, got[0].FigaroLT)
		}
		below := c.ReadFrom(boundary-1, 2)
		exactlyOnceInOrder(t, "ReadFrom(boundary-1)", below)
		contiguous(t, "ReadFrom(boundary-1)", below)
	})
}

// The boundary MOVES while reads are in flight, which is when a seam bug
// actually happens: a trim republishes the window between a reader's decision
// about which side owns an LT and its read of that side.
func TestSeamHoldsWhileTheBoundaryMoves(t *testing.T) {
	c := seamLog(t, 2000)

	stop := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					errs <- nil
					return
				default:
				}
				got := c.ReadFrom(900, 300)
				seen := make(map[uint64]struct{}, len(got))
				for i, e := range got {
					if _, dup := seen[e.FigaroLT]; dup {
						errs <- fmt.Errorf("LT %d returned twice across a moving seam", e.FigaroLT)
						return
					}
					seen[e.FigaroLT] = struct{}{}
					if i > 0 && e.FigaroLT != got[i-1].FigaroLT+1 {
						errs <- fmt.Errorf("seam broke: %d follows %d", e.FigaroLT, got[i-1].FigaroLT)
						return
					}
				}
			}
		}()
	}

	for i := 0; i < 300; i++ {
		c.Append(Entry[string]{Payload: fmt.Sprintf("new-%04d", i)})
		c.Trim(500 + i%400) // walk the boundary back and forth under the readers
	}
	close(stop)
	wg.Wait()
	for r := 0; r < 8; r++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
