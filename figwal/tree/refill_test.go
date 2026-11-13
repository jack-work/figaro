package tree

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

func sourceUnit(node string, k uint64) unit {
	return unit{k: k, s: fmt.Sprintf("%s-%d-%s", node, k, strings.Repeat("x", 1024))}
}

// A node of single-unit runs, hollowed by eviction, is read back by Range. Runs
// never overlap here, so the walk visits every run a span touches. The
// stretch refill must answer what the per-run refill answered: the source's
// units for the whole span, every run the span touches resident afterwards,
// every run that was resident before it untouched (same object), and the
// budget charged exactly the resident bytes. Puts, Drops, idle trims and
// DropNode interleave at random.
func TestStretchRefillAnswersWhatPerRunRefillDid(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(0)
	c := newCache(b, &calls, &mu)
	rng := rand.New(rand.NewSource(5))
	const span = 400
	lineage := []Ref{{Node: "p"}}

	residentBytes := func() int64 {
		var n int64
		for _, r := range c.runs("p") {
			if r.resident {
				n += r.bytes
			}
		}
		return n
	}

	for step := 0; step < 4000; step++ {
		switch op := rng.Intn(20); {
		case op < 8:
			k := 1 + uint64(rng.Intn(span))
			covered := false
			for _, r := range c.runs("p") {
				if r.coord.From < k && r.coord.To >= k && r.coord != (Coord{Node: "p", From: k - 1, To: k}) {
					covered = true
				}
			}
			if !covered {
				c.Put(Coord{Node: "p", From: k - 1, To: k}, []unit{sourceUnit("p", k)}, rng.Intn(25) == 0)
			}
		case op < 9:
			if runs := c.runs("p"); len(runs) > 0 {
				c.Drop(runs[rng.Intn(len(runs))].coord)
			}
		case op < 11:
			b.TrimIdle(int64(rng.Intn(3)) - 1)
		case op < 12:
			c.DropNode("p")
		default:
			from := uint64(rng.Intn(span))
			to := from + 1 + uint64(rng.Intn(96))
			before := map[*run[unit]]bool{}
			for _, r := range c.runs("p") {
				if r.resident {
					before[r] = true
				}
			}
			got, err := c.Range(lineage, from, to)
			if err != nil {
				t.Fatalf("step %d: Range(%d,%d): %v", step, from, to, err)
			}
			if uint64(len(got)) != to-from {
				t.Fatalf("step %d: Range(%d,%d) answered %d units", step, from, to, len(got))
			}
			for i, u := range got {
				if want := sourceUnit("p", from+1+uint64(i)); u != want {
					t.Fatalf("step %d: Range(%d,%d)[%d] has key %d, want %d", step, from, to, i, u.k, want.k)
				}
			}
			after := map[*run[unit]]bool{}
			for _, r := range c.runs("p") {
				after[r] = true
				if r.coord.From < to && r.coord.To > from && !r.resident {
					t.Fatalf("step %d: run %+v inside Range(%d,%d) is still hollow", step, r.coord, from, to)
				}
			}
			for r := range before {
				if !after[r] {
					t.Fatalf("step %d: Range(%d,%d) replaced the resident run %+v", step, from, to, r.coord)
				}
			}
		}
		if got, want := b.bytes.Load(), residentBytes(); got != want {
			t.Fatalf("step %d: budget charged %d, resident runs hold %d", step, got, want)
		}
	}
}

// Reading back a hollowed node of single-unit runs costs one fetch per
// stretch of up to runChunk units, not one per run.
func TestStretchRefillFetchesOncePerChunk(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := NewBudget(0)
	c := newCache(b, &calls, &mu)
	const n = 4 * runChunk
	for k := uint64(1); k <= n; k++ {
		c.Put(Coord{Node: "p", From: k - 1, To: k}, []unit{sourceUnit("p", k)}, false)
	}
	b.TrimIdle(-1)
	calls = 0
	got, err := c.Range([]Ref{{Node: "p"}}, 0, n)
	if err != nil || len(got) != n {
		t.Fatalf("read back %d units, err %v", len(got), err)
	}
	if calls != n/runChunk {
		t.Fatalf("reading back %d hollow single-unit runs made %d source calls, want %d", n, calls, n/runChunk)
	}
}
