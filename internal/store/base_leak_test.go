package store

import (
	"fmt"
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// A run materialized in the ANCESTOR can span a child's fork base: nothing
// stops one covering 250-320 when the child forked at 299. Records at or above
// the base are, in the parent, its own POST-FORK continuation -- a different
// conversation. If the child sees them it reads as correct data from the wrong
// branch: the sibling-leak shape in its new home.
//
// The seam moved to the fork base, so this is where that hazard now lives.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.

func leakCache(t *testing.T) *fwtree.Cache[string] {
	t.Helper()
	return fwtree.New(
		func(c fwtree.Coord) ([]string, error) {
			out := make([]string, 0, c.To-c.From)
			for i := c.From + 1; i <= c.To; i++ {
				out = append(out, fmt.Sprintf("%s@%d", c.Node, i))
			}
			return out, nil
		},
		fwtree.NewBudget(1<<20),
		func(s string) int { return len(s) + 16 },
		func(s string) uint64 {
			var n uint64
			for i := len(s) - 1; i >= 0; i-- {
				if s[i] == '@' {
					fmt.Sscanf(s[i+1:], "%d", &n)
					break
				}
			}
			return n
		},
	)
}

func TestAncestorRunSpanningTheBaseDoesNotLeakIntoTheChild(t *testing.T) {
	const base = 299
	c := leakCache(t)
	defer c.Close()

	// The parent materializes its own history straight across the fork point,
	// including records it wrote AFTER the child diverged.
	units := make([]string, 0, 70)
	for i := uint64(251); i <= 320; i++ {
		units = append(units, fmt.Sprintf("parent@%d", i))
	}
	c.Put(fwtree.Coord{Node: "parent", From: 250, To: 320}, units, false)

	lineage := []fwtree.Ref{{Node: "parent", Base: 0}, {Node: "child", Base: base}}

	t.Run("below the base only", func(t *testing.T) {
		got, err := c.Range(lineage, 250, base-1)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range got {
			var lt uint64
			fmt.Sscanf(u[len("parent@"):], "%d", &lt)
			if lt >= base {
				t.Fatalf("child read %q, at or above its base %d: the parent's "+
					"post-fork continuation leaked into the child's past", u, base)
			}
		}
		if len(got) == 0 {
			t.Fatal("read nothing; the fixture cannot show a leak")
		}
	})

	t.Run("across the base", func(t *testing.T) {
		got, err := c.Range(lineage, 250, base+5)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range got {
			var lt uint64
			sep := 0
			for i := len(u) - 1; i >= 0; i-- {
				if u[i] == '@' {
					sep = i
					break
				}
			}
			fmt.Sscanf(u[sep+1:], "%d", &lt)
			node := u[:sep]
			if lt < base && node != "parent" {
				t.Errorf("record %d below the base came from %q, want parent", lt, node)
			}
			if lt >= base && node != "child" {
				t.Errorf("record %d at or above the base came from %q, want child -- "+
					"this is the parent's own continuation, a different conversation", lt, node)
			}
		}
	})
}

// The same, with the ancestor's spanning run already WARM, since a hit slices
// an existing run rather than re-materializing and that is a different path.
func TestWarmAncestorRunRespectsTheBase(t *testing.T) {
	const base = 100
	c := leakCache(t)
	defer c.Close()

	units := make([]string, 0, 100)
	for i := uint64(51); i <= 150; i++ {
		units = append(units, fmt.Sprintf("parent@%d", i))
	}
	c.Put(fwtree.Coord{Node: "parent", From: 50, To: 150}, units, false)

	lineage := []fwtree.Ref{{Node: "parent", Base: 0}, {Node: "child", Base: base}}
	for i := 0; i < 3; i++ { // warm it, then read again
		got, err := c.Range(lineage, 50, base-1)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range got {
			var lt uint64
			fmt.Sscanf(u[len("parent@"):], "%d", &lt)
			if lt >= base {
				t.Fatalf("pass %d: warm slice leaked %q past the base", i, u)
			}
		}
	}
}
