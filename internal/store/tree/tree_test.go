package tree

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

type unit struct {
	k uint64
	s string
}

func mkSource(node string, calls *int, mu *sync.Mutex) Source[unit] {
	return func(c Coord) ([]unit, error) {
		mu.Lock()
		*calls++
		mu.Unlock()
		var out []unit
		for k := c.From + 1; k <= c.To; k++ {
			out = append(out, unit{k: k, s: fmt.Sprintf("%s-%d-%s", node, k, strings.Repeat("x", 1024))})
		}
		return out, nil
	}
}

func newCache(b *Budget, calls *int, mu *sync.Mutex) *Cache[unit] {
	return New(func(c Coord) ([]unit, error) { return mkSource(c.Node, calls, mu)(c) },
		b, func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
}

// A fork's read of the shared prefix hits the ancestor's runs: one
// materialize serves both branches, and the second branch's read costs
// zero source calls. THE point of the tree.
func TestPrefixResidencyIsShared(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newCache(NewBudget(0), &calls, &mu)
	parentThenChildA := []Ref{{Node: "p"}, {Node: "a", Base: 51}}
	parentThenChildB := []Ref{{Node: "p"}, {Node: "b", Base: 51}}

	ua, err := c.Range(parentThenChildA, 0, 80)
	if err != nil || len(ua) != 80 {
		t.Fatalf("branch a: %d units err=%v", len(ua), err)
	}
	before := calls
	ub, err := c.Range(parentThenChildB, 0, 50) // entirely within the shared prefix
	if err != nil || len(ub) != 50 {
		t.Fatalf("branch b: %d units err=%v", len(ub), err)
	}
	if calls != before {
		t.Fatalf("branch b re-materialized the shared prefix: %d extra source calls", calls-before)
	}
	if ub[0].s[:1] != "p" {
		t.Fatalf("the prefix must be the PARENT's units: %q", ub[0].s[:8])
	}
}

// Eviction hollows a run but keeps its index; the next read in it
// rematerializes exactly that coord and the bytes return to the meter.
func TestIndexSurvivesEviction(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(64 << 10) // 64KB: ~60 units of ~1KB fit
	c := newCache(b, &calls, &mu)
	lin := []Ref{{Node: "n"}}
	if _, err := c.Range(lin, 0, 200); err != nil { // ~200KB >> budget
		t.Fatal(err)
	}
	res, _, ev := b.Stats()
	if ev == 0 {
		t.Fatal("no evictions under a 3x-over budget")
	}
	if res > 96<<10 {
		t.Fatalf("resident %d far exceeds budget", res)
	}
	before := calls
	u, err := c.Range(lin, 0, 10)
	if err != nil || len(u) != 10 {
		t.Fatalf("re-read of evicted head: %d err=%v", len(u), err)
	}
	if calls == before {
		t.Fatal("an evicted range must rematerialize (source untouched)")
	}
}

// A pinned run is never evicted and its bytes stay on the meter: count
// what you pin (S1's law).
func TestPinsAreCountedNotEvicted(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(8 << 10)
	c := newCache(b, &calls, &mu)
	var pinnedUnits []unit
	for k := uint64(1); k <= 4; k++ {
		pinnedUnits = append(pinnedUnits, unit{k: k, s: strings.Repeat("p", 4<<10)})
	}
	c.Put(Coord{"n", 0, 4}, pinnedUnits, true) // 16KB pinned, over an 8KB budget
	if _, err := c.Range([]Ref{{Node: "n"}}, 4, 20); err != nil {
		t.Fatal(err)
	}
	res, _, _ := b.Stats()
	if res < 16<<10 {
		t.Fatalf("pinned bytes fell off the meter: %d", res)
	}
	u, err := c.Range([]Ref{{Node: "n"}}, 0, 4)
	if err != nil || len(u) != 4 || len(u[0].s) != 4<<10 {
		t.Fatalf("the pinned run must stay resident: %d err=%v", len(u), err)
	}
}

// One budget, two caches: the coldest run across BOTH is the victim.
func TestBudgetIsSharedAcrossCaches(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(32 << 10)
	c1 := newCache(b, &calls, &mu)
	c2 := newCache(b, &calls, &mu)
	if _, err := c1.Range([]Ref{{Node: "old"}}, 0, 20); err != nil {
		t.Fatal(err)
	}
	filled := 0
	for _, r := range c1.Index("old") {
		if r.Resident {
			filled++
		}
	}
	if filled == 0 {
		t.Fatal("fixture: c1 holds nothing to lose")
	}
	if _, err := c2.Range([]Ref{{Node: "new"}}, 0, 20); err != nil {
		t.Fatal(err)
	}
	// THE CLAIM: pressure from c2 reaches INTO c1. Not that c1 keeps nothing
	// colder -- eviction stops as soon as the budget fits, so it may -- but
	// that a second cache sharing the budget costs the first its residency.
	after := 0
	for _, r := range c1.Index("old") {
		if r.Resident {
			after++
		}
	}
	if after >= filled {
		t.Fatalf("c2's insert cost c1 nothing: %d resident runs before, %d after", filled, after)
	}
	c1.Close()
	res, _, _ := b.Stats()
	if res > 32<<10 {
		t.Fatalf("Close must return its bytes: %d resident", res)
	}
}

// Concurrent readers over one cache: -race is the assertion.
func TestConcurrentRange(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newCache(NewBudget(128<<10), &calls, &mu)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			lin := []Ref{{Node: "n"}}
			for i := 0; i < 50; i++ {
				lo := uint64((g * 17) % 100)
				if _, err := c.Range(lin, lo, lo+30); err != nil {
					t.Error(err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
