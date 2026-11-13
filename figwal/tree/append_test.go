package tree

import (
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

func maxToHolds(runs []*run[unit], maxTo []uint64) bool {
	if len(maxTo) != len(runs) {
		return false
	}
	var reach uint64
	for i, r := range runs {
		reach = max(reach, r.coord.To)
		if maxTo[i] != reach {
			return false
		}
	}
	return true
}

func tailUnit(k uint64) unit { return unit{k: k, s: fmt.Sprintf("u%d", k)} }

func putOne(c *Cache[unit], node string, k uint64) {
	c.Put(Coord{Node: node, From: k - 1, To: k}, []unit{tailUnit(k)}, false)
}

// The index after any mix of tail appends, middle inserts and exact
// replacements must equal what replace alone builds: sorted by From, one run
// per coord, and maxTo[i] the largest To of runs[0..i]. Defends the fast path
// against drifting from the slow one, and against runs and maxTo growing apart.
func TestTailAppendsBuildTheIndexReplaceWould(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newCache(NewBudget(0), &calls, &mu)
	rng := rand.New(rand.NewSource(7))

	want := map[Coord]bool{}
	var tail uint64
	for i := 0; i < 4000; i++ {
		var co Coord
		switch rng.Intn(6) {
		case 0:
			if tail < 4 {
				continue
			}
			k := 1 + uint64(rng.Int63n(int64(tail-1)))
			co = Coord{Node: "p", From: k - 1, To: k}
		case 1:
			if tail < 4 {
				continue
			}
			k := 1 + uint64(rng.Int63n(int64(tail-1)))
			co = Coord{Node: "p", From: k - 1, To: k + 1}
		default:
			tail += 1 + uint64(rng.Intn(3))
			co = Coord{Node: "p", From: tail - 1, To: tail}
		}
		units := make([]unit, 0, co.To-co.From)
		for k := co.From + 1; k <= co.To; k++ {
			units = append(units, tailUnit(k))
		}
		c.Put(co, units, false)
		want[co] = true
		if ix := c.lookup("p").index(); !maxToHolds(ix.runs, ix.maxTo) {
			t.Fatalf("after put %d at %+v: maxTo %v does not reach over runs", i, co, ix.maxTo[max(0, len(ix.maxTo)-3):])
		}
	}

	ix := c.lookup("p").index()
	if len(ix.runs) != len(want) || len(ix.maxTo) != len(ix.runs) {
		t.Fatalf("index holds %d runs and %d maxTo; want %d of each", len(ix.runs), len(ix.maxTo), len(want))
	}
	if !sort.SliceIsSorted(ix.runs, func(i, j int) bool { return ix.runs[i].coord.From < ix.runs[j].coord.From }) {
		t.Fatal("runs are not sorted by From")
	}
	for i, r := range ix.runs {
		if !want[r.coord] {
			t.Fatalf("run %d at %+v was never put", i, r.coord)
		}
		delete(want, r.coord)
	}
	for k := uint64(1); k <= tail; k++ {
		if u, ok := c.At("p", k); ok && u.k != k {
			t.Fatalf("At(%d) answered unit %d", k, u.k)
		}
	}
	if u, ok := c.At("p", tail); !ok || u.k != tail {
		t.Fatalf("the last appended unit %d is not served: %+v %v", tail, u, ok)
	}
}

// A reader holding a published runIndex must see exactly what it saw when it
// loaded it, however many runs are appended into spare capacity, inserted,
// replaced, dropped or evicted afterwards. Run under -race: a writer touching a
// published backing array at or below a held header's len is a data race
// here, and so is an element written after the header is stored.
func TestAHeldIndexNeverChangesUnderAppends(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(64 << 10)
	c := newCache(b, &calls, &mu)
	putOne(c, "p", 1)

	var stop atomic.Bool
	var readers sync.WaitGroup
	for g := 0; g < 4; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				ix := c.lookup("p").index()
				runs := append([]*run[unit](nil), ix.runs...)
				tos := append([]uint64(nil), ix.maxTo...)
				for i := range runs {
					if runs[i] == nil {
						t.Errorf("held index: run %d is nil", i)
						return
					}
				}
				if !maxToHolds(runs, tos) {
					t.Errorf("held index: maxTo does not reach over its %d runs", len(runs))
					return
				}
				runtime.Gosched()
				if len(ix.runs) != len(runs) || len(ix.maxTo) != len(tos) {
					t.Errorf("held index changed length")
					return
				}
				for i := range runs {
					if ix.runs[i] != runs[i] || ix.maxTo[i] != tos[i] {
						t.Errorf("held index changed at %d", i)
						return
					}
				}
				if top := tos[len(tos)-1]; top > 16 {
					_, _ = c.Range([]Ref{{Node: "p"}}, top-16, top)
				}
			}
		}()
	}

	first := c.lookup("p").index()
	firstRuns := append([]*run[unit](nil), first.runs...)
	rng := rand.New(rand.NewSource(11))
	tail := uint64(1)
	for i := 0; i < 3000; i++ {
		tail += 2
		putOne(c, "p", tail)
		switch rng.Intn(8) {
		case 0:
			k := 2 * (1 + uint64(rng.Int63n(int64(tail/2))))
			putOne(c, "p", k)
		case 1:
			k := 1 + 2*uint64(rng.Int63n(int64(tail/2)))
			c.Drop(Coord{Node: "p", From: k - 1, To: k})
		case 2:
			b.Sweep()
		}
	}
	stop.Store(true)
	readers.Wait()

	if len(first.runs) != len(firstRuns) || first.runs[0] != firstRuns[0] {
		t.Fatalf("the first published index changed: %d runs, want %d", len(first.runs), len(firstRuns))
	}
}

// An append onto a node that already holds thousands of runs costs what it
// costs on an empty one: the run index grows in place rather than being
// copied and rebuilt per Put.
func TestTailAppendBytesAreFlatInHeldRuns(t *testing.T) {
	perAppend := func(held int) float64 {
		var calls int
		var mu sync.Mutex
		c := newCache(NewBudget(0), &calls, &mu)
		k := uint64(0)
		for ; k < uint64(held); k++ {
			putOne(c, "p", k+1)
		}
		const appends = 2000
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < appends; i++ {
			k++
			putOne(c, "p", k)
		}
		runtime.ReadMemStats(&after)
		return float64(after.TotalAlloc-before.TotalAlloc) / appends
	}
	empty := perAppend(0)
	long := perAppend(8000)
	if long > empty+1024 {
		t.Fatalf("an append onto 8000 held runs allocates %.0f B, onto none %.0f B: the index is copied per Put", long, empty)
	}
}
