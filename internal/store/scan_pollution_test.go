package store

import (
	"fmt"
	"testing"

	fwforest "github.com/jack-work/figaro/internal/store/tree"
)

// SCAN POLLUTION, measured: a whole-history read through forest.Cache DOES
// evict other nodes' hot tails. Both tests below assert that it happens.
//
// forest is not at fault -- a cache that caches what it reads is correct, and
// bounding a scan is not something it promises. The conclusion is a POLICY for
// the consumer: only bounded reads near the window are routed through the
// cache; a whole-history read passes through to the source, retaining nothing.
//
// cachedLog already states that policy for backward paging in its own words --
// "a scroll must not permanently re-resident a prefix nobody will read again"
// -- and the re-seat must extend it rather than route everything through
// Range. These tests are why.
//
// If forest ever bounds scans itself, these fail, and the policy can be
// simplified. That is the point of asserting the fact rather than the wish.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.

type scanFixture struct {
	cache  *fwforest.Cache[string]
	source func(fwforest.Coord) ([]string, error)
	calls  map[string]int
}

func newScanFixture(budgetBytes int64) *scanFixture {
	f := &scanFixture{calls: map[string]int{}}
	f.source = func(c fwforest.Coord) ([]string, error) {
		f.calls[c.Node]++
		out := make([]string, 0, c.To-c.From)
		for i := c.From + 1; i <= c.To; i++ {
			out = append(out, fmt.Sprintf("%s@%08d%s", c.Node, i, string(make([]byte, 512))))
		}
		return out, nil
	}
	f.cache = fwforest.New(f.source, fwforest.NewBudget(budgetBytes),
		func(s string) int { return len(s) + 16 },
		func(s string) uint64 { return 0 })
	return f
}

func (f *scanFixture) read(node string, from, to uint64) int {
	got, err := f.cache.Range([]fwforest.Ref{{Node: node, Base: 0}}, from, to)
	if err != nil {
		panic(err)
	}
	return len(got)
}

// The neighbours' tails are small and hot; the scan is huge and one-shot.
// That is figaro's actual workload: many small hot tails, rare giant reads.
func TestWholeHistoryReadEvictsOtherAriasTails(t *testing.T) {
	const (
		tailUnits = 40
		history   = 4000
		budget    = 512 << 10 // holds both tails many times over, not the history
	)
	f := newScanFixture(budget)
	defer f.cache.Close()

	f.read("ariaA", 0, tailUnits)
	f.read("ariaB", 0, tailUnits)

	// Warm: a re-read must cost no source call, or the fixture proves nothing.
	before := map[string]int{"ariaA": f.calls["ariaA"], "ariaB": f.calls["ariaB"]}
	f.read("ariaA", 0, tailUnits)
	f.read("ariaB", 0, tailUnits)
	if f.calls["ariaA"] != before["ariaA"] || f.calls["ariaB"] != before["ariaB"] {
		t.Fatalf("tails were not warm before the scan (A %d->%d, B %d->%d); fixture is wrong",
			before["ariaA"], f.calls["ariaA"], before["ariaB"], f.calls["ariaB"])
	}

	// The scan: one aria's whole history, far larger than the budget.
	f.read("ariaC", 0, history)

	warm := map[string]int{"ariaA": f.calls["ariaA"], "ariaB": f.calls["ariaB"]}
	f.read("ariaA", 0, tailUnits)
	f.read("ariaB", 0, tailUnits)

	lostA := f.calls["ariaA"] - warm["ariaA"]
	lostB := f.calls["ariaB"] - warm["ariaB"]
	if lostA == 0 && lostB == 0 {
		t.Fatal("forest now bounds a whole-history scan; the pass-through policy " +
			"in cachedLog can be simplified, and this test should say so")
	}
	t.Logf("scan pollution confirmed: one whole-history read cost the neighbours "+
		"%d and %d rematerializations -- hence the pass-through policy", lostA, lostB)
}

// The same shape with the scan repeated, since one pass may fit where a
// second does not, and a listing walks many arias in a row.
func TestRepeatedScansEvictOtherAriasTails(t *testing.T) {
	const (
		tailUnits = 40
		history   = 2000
		budget    = 512 << 10
	)
	f := newScanFixture(budget)
	defer f.cache.Close()

	f.read("ariaA", 0, tailUnits)
	f.read("ariaB", 0, tailUnits)
	warm := map[string]int{"ariaA": f.calls["ariaA"], "ariaB": f.calls["ariaB"]}

	for i := 0; i < 5; i++ {
		f.read(fmt.Sprintf("scan%d", i), 0, history)
	}

	f.read("ariaA", 0, tailUnits)
	f.read("ariaB", 0, tailUnits)
	lostA := f.calls["ariaA"] - warm["ariaA"]
	lostB := f.calls["ariaB"] - warm["ariaB"]
	if lostA == 0 && lostB == 0 {
		t.Fatal("forest now bounds listing-shaped scans; revisit the pass-through policy")
	}
	t.Logf("listing-shaped load: neighbours cost %d and %d rematerializations "+
		"across 5 whole-history reads", lostA, lostB)
}
