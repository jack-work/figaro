package store

import (
	"fmt"
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// Pins the fork-base convention across three parties that must agree, because
// if they ever disagree nothing fails loudly: a fork reads one record of its
// sibling's history, which looks like correct data from the wrong branch.
//
// Measured agreement on a live store (aria 94f0752b, node n856):
// figwal .fork marker base=3, first record present in the node _idx=3,
// figaro branched_lt=3. Base is the FIRST coordinate the child owns.

func forkLineage(base uint64) []fwtree.Ref {
	return []fwtree.Ref{{Node: "parent", Base: 0}, {Node: "child", Base: base}}
}

// nodeSource answers with the node that served each unit, so a read can be
// attributed to a branch rather than merely checked for content.
func nodeSource(t *testing.T, served map[string]int) fwtree.Source[string] {
	t.Helper()
	return func(c fwtree.Coord) ([]string, error) {
		served[c.Node]++
		out := make([]string, 0, c.To-c.From)
		for i := c.From + 1; i <= c.To; i++ {
			out = append(out, fmt.Sprintf("%s@%d", c.Node, i))
		}
		return out, nil
	}
}

func TestForkBaseSplitsAtTheChildsFirstRecord(t *testing.T) {
	const base = 3
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		t.Run(name, func(t *testing.T) {
			served := map[string]int{}
			c := fwtree.New(nodeSource(t, served), fwtree.NewBudget(1<<20),
				func(s string) int { return len(s) + 16 },
				func(s string) uint64 { var n uint64; fmt.Sscanf(s[len(s)-1:], "%d", &n); return n })
			defer c.Close()

			if warm {
				if _, err := c.Range(forkLineage(base), 0, 6); err != nil {
					t.Fatal(err)
				}
			}

			got, err := c.Range(forkLineage(base), 0, 6)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 6 {
				t.Fatalf("read %d units, want 6: %v", len(got), got)
			}

			// Records 1..base-1 are the PARENT's; base.. are the CHILD's.
			for i, u := range got {
				lt := uint64(i + 1)
				wantNode := "child"
				if lt < base {
					wantNode = "parent"
				}
				if want := fmt.Sprintf("%s@%d", wantNode, lt); u != want {
					t.Errorf("record %d resolved to %q, want %q", lt, u, want)
				}
			}
		})
	}
}

// The boundary, stated on its own so a failure names the off-by-one directly.
func TestForkBaseBoundaryRecordBelongsToTheChild(t *testing.T) {
	const base = 5
	served := map[string]int{}
	c := fwtree.New(nodeSource(t, served), fwtree.NewBudget(1<<20),
		func(s string) int { return len(s) + 16 },
		func(s string) uint64 { var n uint64; fmt.Sscanf(s[len(s)-1:], "%d", &n); return n })
	defer c.Close()

	got, err := c.Range(forkLineage(base), base-2, base) // records base-1 and base
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d units, want 2: %v", len(got), got)
	}
	if got[0] != fmt.Sprintf("parent@%d", base-1) {
		t.Errorf("record base-1 = %q; the parent must still own it", got[0])
	}
	if got[1] != fmt.Sprintf("child@%d", base) {
		t.Errorf("record base = %q; the child must own its own base", got[1])
	}
}

// The point of the tree: N branches of one trunk pay ONE residency for the
// shared prefix. Today the decoded IR is per-aria flat and each fork pays
// separately.
func TestForksShareOnePrefixResidency(t *testing.T) {
	served := map[string]int{}
	c := fwtree.New(nodeSource(t, served), fwtree.NewBudget(1<<20),
		func(s string) int { return len(s) + 16 },
		func(s string) uint64 { var n uint64; fmt.Sscanf(s[len(s)-1:], "%d", &n); return n })
	defer c.Close()

	a := []fwtree.Ref{{Node: "parent", Base: 0}, {Node: "childA", Base: 8}}
	b := []fwtree.Ref{{Node: "parent", Base: 0}, {Node: "childB", Base: 8}}

	if _, err := c.Range(a, 0, 7); err != nil {
		t.Fatal(err)
	}
	afterFirst := served["parent"]
	if afterFirst == 0 {
		t.Fatal("the prefix was never materialized")
	}
	if _, err := c.Range(b, 0, 7); err != nil {
		t.Fatal(err)
	}
	if served["parent"] != afterFirst {
		t.Errorf("sibling re-materialized the shared prefix (%d -> %d source calls); "+
			"prefix sharing is the whole point of the tree", afterFirst, served["parent"])
	}
}
