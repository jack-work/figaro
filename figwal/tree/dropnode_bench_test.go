package tree

import (
	"fmt"
	"testing"
)

// One teardown of a node with resident runs, plus the read that refills it.
func BenchmarkDropNodeAndRefill(b *testing.B) {
	c := New(func(co Coord) ([]unit, error) {
		out := make([]unit, 0, co.To-co.From)
		for k := co.From + 1; k <= co.To; k++ {
			out = append(out, unit{k: k, s: fmt.Sprint(k)})
		}
		return out, nil
	}, NewBudget(0), func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
	h := c.Node("p")
	for b.Loop() {
		if _, err := h.Range(0, 1024); err != nil {
			b.Fatal(err)
		}
		c.DropNode("p")
	}
}
