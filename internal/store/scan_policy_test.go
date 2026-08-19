package store

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// The SUBJECT here is figaro, not tree.
//
// scan_pollution_test.go establishes a fact about tree: route a scan through
// Range and it evicts other nodes. This asserts the fact that actually
// protects a user -- a whole-history read through cachedLog's PUBLIC surface
// does not cost the neighbours their residency, because it is not routed
// through the cache at all.
//
// That is the one the re-seat can silently break: point one belowWindow branch
// at the cache instead of the source and the policy is gone with nothing red.
// Green before the re-seat and green after, or the policy did not survive.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.

// countingLog counts what the layer BELOW the window is asked to do, which is
// the only externally visible sign that residency was lost.
type countingLog[T any] struct {
	Log[T]
	reads atomic.Int64
}

func (c *countingLog[T]) Read() []Entry[T] {
	c.reads.Add(1)
	return c.Log.Read()
}

func (c *countingLog[T]) ReadFrom(lt uint64, n int) []Entry[T] {
	c.reads.Add(1)
	return c.Log.ReadFrom(lt, n)
}

func (c *countingLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	c.reads.Add(1)
	return c.Log.ReadPage(from, before, n)
}

func windowedAria(t *testing.T, name string, n, window int) (*cachedLog[string], *countingLog[string]) {
	t.Helper()
	inner := NewMemLog[string]()
	for i := 1; i <= n; i++ {
		if _, err := inner.Append(Entry[string]{Payload: fmt.Sprintf("%s-%06d", name, i)}); err != nil {
			t.Fatal(err)
		}
	}
	counted := &countingLog[string]{Log: inner}
	return newWindowedLog[string](counted, window, 0, 1,
		func(e Entry[string]) int { return len(e.Payload) + 48 }), counted
}

func TestWholeHistoryReadKeepsNeighboursResident(t *testing.T) {
	const window = 40

	a, aInner := windowedAria(t, "ariaA", 200, window)
	b, bInner := windowedAria(t, "ariaB", 200, window)
	c, _ := windowedAria(t, "ariaC", 4000, window)

	// Warm both tails, then establish that a tail read costs the layer below
	// nothing. If it does, the fixture is not measuring residency.
	a.ReadFrom(a.load().rows[0].FigaroLT, window)
	b.ReadFrom(b.load().rows[0].FigaroLT, window)
	aWarm, bWarm := aInner.reads.Load(), bInner.reads.Load()

	a.ReadFrom(a.load().rows[0].FigaroLT, window)
	b.ReadFrom(b.load().rows[0].FigaroLT, window)
	if aInner.reads.Load() != aWarm || bInner.reads.Load() != bWarm {
		t.Fatalf("a warm tail read fell through (A %d->%d, B %d->%d); fixture is wrong",
			aWarm, aInner.reads.Load(), bWarm, bInner.reads.Load())
	}

	// The scan: ariaC's whole history, through the public surface.
	if got := len(c.Read()); got != 4000 {
		t.Fatalf("scan read %d rows, want 4000", got)
	}

	// The assertion: the neighbours' tails are still served from their windows.
	before := [2]int64{aInner.reads.Load(), bInner.reads.Load()}
	a.ReadFrom(a.load().rows[0].FigaroLT, window)
	b.ReadFrom(b.load().rows[0].FigaroLT, window)
	lostA := aInner.reads.Load() - before[0]
	lostB := bInner.reads.Load() - before[1]

	if lostA > 0 || lostB > 0 {
		t.Fatalf("a whole-history read cost the neighbours their residency "+
			"(%d and %d fall-throughs). A scan must pass through to the source, "+
			"not populate the cache: see cachedLog.ReadPage's own rule.", lostA, lostB)
	}
}

// Backward paging is the case cachedLog already decided, and the re-seat must
// not quietly re-decide it: a scroll must not re-resident a prefix nobody will
// read again.
func TestBackwardPagingKeepsNeighboursResident(t *testing.T) {
	const window = 40

	a, aInner := windowedAria(t, "ariaA", 200, window)
	c, _ := windowedAria(t, "ariaC", 4000, window)

	a.ReadFrom(a.load().rows[0].FigaroLT, window)
	warm := aInner.reads.Load()
	a.ReadFrom(a.load().rows[0].FigaroLT, window)
	if aInner.reads.Load() != warm {
		t.Fatal("warm tail read fell through; fixture is wrong")
	}

	// Scroll back through the whole history, a page at a time.
	for before := uint64(3000); before > 100; before -= 100 {
		c.ReadPage(0, before, 100)
	}

	before := aInner.reads.Load()
	a.ReadFrom(a.load().rows[0].FigaroLT, window)
	if lost := aInner.reads.Load() - before; lost > 0 {
		t.Fatalf("a backward scroll cost a neighbour %d fall-throughs; "+
			"paging must be served from the source", lost)
	}
}
