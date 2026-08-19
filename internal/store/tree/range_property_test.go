package tree

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// A PROPERTY TEST OVER THE PUBLISHED INDEX, written because the read path was
// rewritten from a locked walk into a lock-free one and the existing tests
// exercise the shapes their authors thought of. The invariant is the only one
// that matters to a caller:
//
//	A RANGE RETURNS EXACTLY THE UNITS ITS SPAN NAMES, whatever residency did.
//
// Residency is not asserted at all -- a hit, a refill and a cold materialize
// must be INDISTINGUISHABLE in the answer, which is precisely what a cache is
// promising. The randomness drives eviction, refill, replacement at an exact
// coord, and Drop, against a budget small enough that runs are being hollowed
// under the reads.
func TestRangeAnswersItsSpanUnderRandomResidency(t *testing.T) {
	const universe = 600

	canonical := func(c Coord) []unit {
		var out []unit
		for k := c.From + 1; k <= c.To && k <= universe; k++ {
			out = append(out, unit{k: k, s: fmt.Sprintf("p-%d", k)})
		}
		return out
	}

	for seed := int64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewSource(seed))
		b := NewBudget(2 << 10) // small: eviction runs constantly
		c := New(func(co Coord) ([]unit, error) { return canonical(co), nil },
			b, func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
		lineage := []Ref{{Node: "p"}}

		for op := 0; op < 400; op++ {
			from := uint64(rng.Intn(universe))
			to := from + uint64(rng.Intn(80))
			if to > universe {
				to = universe
			}
			switch rng.Intn(6) {
			case 0:
				c.Put(Coord{"p", from, to}, canonical(Coord{"p", from, to}), rng.Intn(8) == 0)
			case 1:
				c.Drop(Coord{"p", from, to})
			case 2:
				b.TrimIdle(int64(rng.Intn(3)))
			default:
				got, err := c.Range(lineage, from, to)
				if err != nil {
					t.Fatalf("seed %d op %d: %v", seed, op, err)
				}
				want := canonical(Coord{"p", from, to})
				if len(got) != len(want) {
					t.Fatalf("seed %d op %d: span (%d..%d] returned %d units, want %d",
						seed, op, from, to, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("seed %d op %d: span (%d..%d] unit %d = %+v, want %+v",
							seed, op, from, to, i, got[i], want[i])
					}
				}
			}
		}
		c.Close()
		b.Settle(2 * time.Second)
		if resident, _, _ := b.Stats(); resident != 0 {
			t.Fatalf("seed %d: %d bytes still charged after Close -- the accountant holds a ghost", seed, resident)
		}
	}
}
