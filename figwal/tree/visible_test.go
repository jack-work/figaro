package tree

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"
)

// ownerByChain answers "which node serves coordinate k" the way the disk does:
// from the leaf upward, a node serves k when k is at or above its own base, and
// its parent is asked otherwise.
func ownerByChain(lineage []Ref, k uint64) string {
	for i := len(lineage) - 1; i > 0; i-- {
		if lineage[i].Base > 0 && k >= lineage[i].Base {
			return lineage[i].Node
		}
	}
	return lineage[0].Node
}

func ownersBySplit(c *Cache[unit], lineage []Ref, from, to uint64) map[uint64]string {
	out := map[uint64]string{}
	for _, cut := range c.split(lineage, from, to) {
		for k := cut.From + 1; k <= cut.To; k++ {
			if prev, dup := out[k]; dup {
				out[k] = prev + "+" + cut.Node
				continue
			}
			out[k] = cut.Node
		}
	}
	return out
}

// A builder forked at an early turn and seated under a successor that forked
// later: the lineage's bases go down at the builder. Every coordinate must
// still come from exactly one node, the one the disk chain would read.
func TestSplitFollowsTheChainWhenABaseGoesDown(t *testing.T) {
	c := New(func(Coord) ([]unit, error) { return nil, nil }, nil,
		func(unit) int { return 1 }, func(u unit) uint64 { return u.k })
	lineage := []Ref{{Node: "holder"}, {Node: "s1", Base: 17}, {Node: "s2", Base: 18}, {Node: "s3", Base: 21}, {Node: "lead", Base: 6}, {Node: "builder", Base: 6}}
	got := ownersBySplit(c, lineage, 0, 30)
	for k := uint64(1); k <= 30; k++ {
		want := "builder"
		if k < 6 {
			want = "holder"
		}
		if got[k] != want {
			t.Fatalf("coordinate %d is served by %q, want %q", k, got[k], want)
		}
	}
}

func TestSplitMatchesTheChainOnRandomLineages(t *testing.T) {
	c := New(func(Coord) ([]unit, error) { return nil, nil }, nil,
		func(unit) int { return 1 }, func(u unit) uint64 { return u.k })
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 4000; trial++ {
		lineage := []Ref{{Node: "n0"}}
		for i := 1; i <= rng.Intn(7); i++ {
			lineage = append(lineage, Ref{Node: fmt.Sprintf("n%d", i), Base: uint64(1 + rng.Intn(24))})
		}
		from := uint64(rng.Intn(20))
		to := from + uint64(rng.Intn(20))
		got := ownersBySplit(c, lineage, from, to)
		for k := from + 1; k <= to; k++ {
			if want := ownerByChain(lineage, k); got[k] != want {
				t.Fatalf("lineage %+v span (%d..%d]: coordinate %d served by %q, want %q", lineage, from, to, k, got[k], want)
			}
		}
		if len(got) != int(to-from) {
			t.Fatalf("lineage %+v span (%d..%d]: %d coordinates served", lineage, from, to, len(got))
		}
	}
}

func TestVisibleReturnsAnOrderedLineageUntouched(t *testing.T) {
	lineage := []Ref{{Node: "a"}, {Node: "b", Base: 4}, {Node: "c", Base: 4}, {Node: "d", Base: 9}}
	got := Visible(lineage)
	if &got[0] != &lineage[0] || !slices.Equal(got, lineage) {
		t.Fatalf("an ordered lineage was rewritten: %+v", got)
	}
	if n := testing.AllocsPerRun(100, func() { Visible(lineage) }); n != 0 {
		t.Fatalf("an ordered lineage costs %v allocations", n)
	}
}
