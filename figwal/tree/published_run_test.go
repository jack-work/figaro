package tree

import (
	"strings"
	"testing"
)

// DropNode hands back exactly the bytes the dropped runs were charged.
func TestDropNodeReturnsTheBudgetToItsLevelBeforeTheRead(t *testing.T) {
	b := NewBudget(0)
	c := New(func(co Coord) ([]unit, error) {
		var out []unit
		for k := co.From + 1; k <= co.To; k++ {
			out = append(out, unit{k: k, s: strings.Repeat("x", 100)})
		}
		return out, nil
	}, b, func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
	other := c.Node("q")
	if _, err := other.Range(0, 10); err != nil {
		t.Fatal(err)
	}
	before, _, _ := b.Stats()
	h := c.Node("p")
	if _, err := h.Range(0, 200); err != nil {
		t.Fatal(err)
	}
	if mid, _, _ := b.Stats(); mid <= before {
		t.Fatalf("the read charged nothing: %d -> %d", before, mid)
	}
	c.DropNode("p")
	if after, _, _ := b.Stats(); after != before {
		t.Fatalf("budget %d after DropNode, want %d", after, before)
	}
	c.DropNode("p")
	if again, _, _ := b.Stats(); again != before {
		t.Fatalf("a second DropNode moved the budget to %d, want %d", again, before)
	}
}
