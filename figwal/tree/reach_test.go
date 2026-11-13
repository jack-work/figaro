package tree

import (
	"math/rand"
	"sync"
	"testing"
)

func linearOverlapping(runs []*run[unit], pos, limit uint64) *run[unit] {
	for _, r := range runs {
		if r.coord.From >= limit {
			break
		}
		if r.coord.To > pos {
			return r
		}
	}
	return nil
}

func linearAt(runs []*run[unit], idx uint64) (unit, bool) {
	for _, r := range runs {
		if r.coord.To < idx {
			continue
		}
		if r.coord.From >= idx || !r.resident {
			return unit{}, false
		}
		for _, u := range r.units {
			if u.k == idx {
				return u, true
			}
		}
		return unit{}, false
	}
	return unit{}, false
}

// The binary searches over maxTo must give the linear scans' answers on a
// node whose runs overlap, nest and share a From, through every publisher:
// Put, a miss's fill, Drop, eviction under a small budget, and DropNode
// hollowing runs in place. A plain per-run To in place of the running maximum
// sends both searches wrong as soon as a short run follows a long one.
func TestReachSearchesMatchTheLinearScanUnderOverlap(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(48 << 10)
	c := newCache(b, &calls, &mu)
	rng := rand.New(rand.NewSource(3))
	const span = 240

	check := func(step int, what string) {
		t.Helper()
		ix := c.lookup("p").index()
		if ix == nil {
			return
		}
		if !maxToHolds(ix.runs, ix.maxTo) {
			t.Fatalf("step %d (%s): maxTo is not the running maximum of To", step, what)
		}
		for q := 0; q < 64; q++ {
			pos := uint64(rng.Intn(span + 8))
			limit := pos + 1 + uint64(rng.Intn(40))
			if got, want := overlapping(ix, pos, limit), linearOverlapping(ix.runs, pos, limit); got != want {
				t.Fatalf("step %d (%s): overlapping(%d, %d) = %v, the linear scan gives %v", step, what, pos, limit, got, want)
			}
		}
		for idx := uint64(0); idx <= span+8; idx++ {
			got, gotOK := c.At("p", idx)
			want, wantOK := linearAt(ix.runs, idx)
			if gotOK != wantOK || got != want {
				t.Fatalf("step %d (%s): At(%d) = %v %v, the linear scan gives %v %v", step, what, idx, got, gotOK, want, wantOK)
			}
			if gotOK && got.k != idx {
				t.Fatalf("step %d (%s): At(%d) answered unit %d", step, what, idx, got.k)
			}
		}
	}

	for step := 0; step < 3000; step++ {
		what := ""
		switch op := rng.Intn(20); {
		case op < 9:
			from := uint64(rng.Intn(span))
			to := from + 1 + uint64(rng.Intn(24))
			units := make([]unit, 0, to-from)
			for k := from + 1; k <= to; k++ {
				units = append(units, tailUnit(k))
			}
			c.Put(Coord{Node: "p", From: from, To: to}, units, rng.Intn(10) == 0)
			what = "put"
		case op < 13:
			from := uint64(rng.Intn(span))
			_, _ = c.Range([]Ref{{Node: "p"}}, from, from+1+uint64(rng.Intn(40)))
			what = "range"
		case op < 16:
			if ix := c.lookup("p").index(); ix != nil && len(ix.runs) > 0 {
				c.Drop(ix.runs[rng.Intn(len(ix.runs))].coord)
			}
			what = "drop"
		case op < 19:
			b.Sweep()
			what = "sweep"
		default:
			c.DropNode("p")
			what = "dropnode"
		}
		check(step, what)
	}
}
