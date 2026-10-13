package store

import (
	"fmt"
	"testing"
)

// A reader holds an immutable published view; hollowing a run must publish a
// successor rather than edit what readers hold. Permanent, not migration
// scaffolding.

func heldEntry(i int) Entry[string] {
	return Entry[string]{Payload: fmt.Sprintf("row-%04d", i)}
}

func heldLog(t *testing.T, n int) *treeLog[string] {
	t.Helper()
	inner := NewMemLog[string]()
	for i := 1; i <= n; i++ {
		if _, err := inner.Append(heldEntry(i)); err != nil {
			t.Fatal(err)
		}
	}
	return newWindowedLog[string](inner, 0, 0, 1, 1, func(e Entry[string]) int { return len(e.Payload) + 48 })
}

// A Read() taken before a Trim must still read correctly after it.
func TestHeldReadSurvivesTrimUnderneath(t *testing.T) {
	c := heldLog(t, 500)

	c.ReadFrom(1, 0) // materialize: Read peeks, it does not populate
	held := c.Read()
	if len(held) != 500 {
		t.Fatalf("held %d rows, want 500", len(held))
	}
	want := make([]string, len(held))
	for i, e := range held {
		want[i] = e.Payload
	}

	if dropped := c.Trim(50); dropped == 0 {
		t.Fatal("trim dropped nothing; the test proves nothing")
	}

	for i, e := range held {
		if e.Payload != want[i] {
			t.Fatalf("row %d mutated under a held Read: %q -> %q", i, want[i], e.Payload)
		}
	}
	// AT MOST keep, not exactly keep: the tree evicts whole runs, so a trim
	// lands on a run boundary rather than on a row count.
	if got := c.Resident(); got > 50 {
		t.Errorf("resident = %d, want at most 50", got)
	}
}

// TailSnapshot returns a subslice of the published array, the sharpest form
// of the hazard: a trim must build a fresh array rather than shift in place.
func TestHeldTailSnapshotSurvivesAppendAndTrim(t *testing.T) {
	c := heldLog(t, 200)

	held := c.TailSnapshot(20)
	want := make([]string, len(held))
	for i, e := range held {
		want[i] = e.Payload
	}

	for i := 0; i < 400; i++ {
		if _, err := c.Append(heldEntry(1000 + i)); err != nil {
			t.Fatal(err)
		}
	}
	c.Trim(10)

	for i, e := range held {
		if e.Payload != want[i] {
			t.Fatalf("row %d of a held TailSnapshot mutated: %q -> %q", i, want[i], e.Payload)
		}
	}
}

// The reader must still see the range it asked for: a hollow that dropped
// rows the held view still indexes would read as truncation.
func TestHeldReadFromIsCompleteAcrossTrim(t *testing.T) {
	c := heldLog(t, 300)

	held := c.ReadFrom(100, 0)
	if len(held) == 0 {
		t.Fatal("nothing held")
	}
	first, last := held[0].FigaroLT, held[len(held)-1].FigaroLT

	c.Trim(5)

	if held[0].FigaroLT != first || held[len(held)-1].FigaroLT != last {
		t.Fatalf("held range moved: [%d,%d] -> [%d,%d]",
			first, last, held[0].FigaroLT, held[len(held)-1].FigaroLT)
	}
	for i := 1; i < len(held); i++ {
		if held[i].FigaroLT <= held[i-1].FigaroLT {
			t.Fatalf("held range lost its order at %d", i)
		}
	}
}

// Concurrent readers holding views across a trim, under -race. The lock-free
// read is only sound if nothing ever writes to a published array.
func TestConcurrentHeldReadsAcrossTrim(t *testing.T) {
	c := heldLog(t, 1000)

	done := make(chan struct{})
	errs := make(chan error, 8)
	for g := 0; g < 8; g++ {
		go func() {
			for {
				select {
				case <-done:
					errs <- nil
					return
				default:
				}
				held := c.ReadFrom(500, 64)
				for i := 1; i < len(held); i++ {
					if held[i].FigaroLT <= held[i-1].FigaroLT {
						errs <- fmt.Errorf("held slice reordered under a concurrent trim")
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		c.Append(heldEntry(2000 + i))
		c.Trim(400)
	}
	close(done)
	for g := 0; g < 8; g++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
