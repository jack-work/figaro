package aria

import (
	"fmt"
	"math/rand"
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// THE THIRD RESIDENCY POLICY, MEASURED AGAINST THE CANONICAL ONE.
//
// plans/tree-shaped-log.md, "the whole board": this package's TurnCache is
// tree.Cache's design written a second time -- hollow entries, an index that
// survives eviction, a source that rematerializes, a budget with the same three
// numbers, per-turn pinning that stays counted. The reading was done there;
// this is the number.
//
// WHAT IS COUNTED: TURNS MATERIALIZED FROM BELOW, under one trace, at one
// budget, by each structure. Counting rather than timing, because the question
// is "how many times", and turns rather than calls because one recompose of a
// contiguous hollow run and one of a single turn are not the same event.
//
// WHAT IS NOT MEASURED: the COST of a recompose (the composed layer's is a
// walk over fig IR, the tree fixture's is a map lookup), the wire, and the
// paginator's own behaviour. This says how often each structure faults, not
// what a fault costs.

func composedFixture(turns, bodyBytes int) []Turn {
	body := string(make([]byte, bodyBytes))
	out := make([]Turn, 0, turns)
	for i := 0; i < turns; i++ {
		out = append(out, Turn{
			ID:      uint64(i + 1),
			LTs:     []uint64{uint64(i*2 + 1), uint64(i*2 + 2)},
			Inquiry: fmt.Sprintf("q-%d%s", i, body),
			Sealed:  true,
		})
	}
	return out
}

func TestComposedLayerFaultsAgainstTheCanonicalCache(t *testing.T) {
	const (
		turns     = 400
		bodyBytes = 8 << 10
		page      = 8
	)
	all := composedFixture(turns, bodyBytes)
	turnSize := turnBytes(all[0])
	budgetBytes := turnSize * 40 // holds a tenth of the history

	// The same hop trace both structures see: tail work, a hop to an older
	// anchor, three pages of locality around it, then the anchor again.
	rng := rand.New(rand.NewSource(20260819))
	type op struct{ lo, hi int }
	var ops []op
	for i := 0; i < 40; i++ {
		ops = append(ops, op{turns - page, turns - 1})
		anchor := rng.Intn(turns - 4*page)
		for k := 0; k < 3; k++ {
			ops = append(ops, op{anchor + k*page/2, anchor + k*page/2 + page - 1})
		}
		ops = append(ops, op{anchor, anchor + page - 1})
	}

	// --- the composed cache, production code ---
	var composedServed int
	budget := &UIBudget{limit: int64(budgetBytes), pending: map[*TurnCache][]int{}}
	cache := NewTurnCache(func(fromLT, toLT uint64) []Turn {
		var got []Turn
		for _, tn := range all {
			if tn.LTs[0] >= fromLT && tn.LTs[1] <= toLT {
				got = append(got, tn)
			}
		}
		composedServed += len(got)
		return got
	}, budget)
	for _, tn := range all {
		cache.Append(tn)
	}
	appendPhase := composedServed
	for _, o := range ops {
		cache.Slice(o.lo, o.hi)
	}

	// --- tree.Cache over the same turns, same budget, same trace ---
	var treeServed int
	b := fwtree.NewBudget(int64(budgetBytes))
	tc := fwtree.New[Turn](
		func(co fwtree.Coord) ([]Turn, error) {
			var got []Turn
			for _, tn := range all {
				if tn.ID > co.From && tn.ID <= co.To {
					got = append(got, tn)
				}
			}
			treeServed += len(got)
			return got, nil
		},
		b,
		func(tn Turn) int { return turnBytes(tn) },
		func(tn Turn) uint64 { return tn.ID },
	)
	defer tc.Close()
	lineage := []fwtree.Ref{{Node: "aria"}}
	for _, o := range ops {
		if _, err := tc.Range(lineage, uint64(o.lo), uint64(o.hi+1)); err != nil {
			t.Fatal(err)
		}
	}

	asked := 0
	for _, o := range ops {
		asked += o.hi - o.lo + 1
	}
	t.Logf("trace: %d reads, %d turns asked for; budget %d bytes holds ~%d turns of %d",
		len(ops), asked, budgetBytes, budgetBytes/turnSize, turnSize)
	t.Logf("COMPOSED (TurnCache): %d turns served from below (%d of them during the append phase), %d recompose calls",
		composedServed, appendPhase, cache.Recomposes())
	t.Logf("CANONICAL (tree):     %d turns served from below, %d source calls",
		treeServed, tc.Recomposes())

	if composedServed == 0 && treeServed == 0 {
		t.Fatal("neither structure faulted: the budget is not binding and the trace measures nothing")
	}
}
