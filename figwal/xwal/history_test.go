package xwal

import (
	"github.com/jack-work/figaro/figwal/tree"
	"testing"
)

func TestServingLineageDropsAncestorsAtOrAboveTheBound(t *testing.T) {
	links := []tree.Ref{{Node: "r"}, {Node: "a", Base: 17}, {Node: "b", Base: 21}, {Node: "c", Base: 6}, {Node: "d", Base: 6}, {Node: "e", Base: 5}}
	got := servingLineage(links)
	want := []string{"r", "e"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want nodes %v", got, want)
	}
	for i, w := range want {
		if got[i].Node != w {
			t.Fatalf("got %v, want nodes %v", got, want)
		}
	}
	rising := []tree.Ref{{Node: "r"}, {Node: "a", Base: 3}, {Node: "b", Base: 9}, {Node: "c", Base: 9}, {Node: "d", Base: 12}}
	if got := servingLineage(rising); len(got) != 4 || got[1].Node != "a" || got[2].Node != "c" || got[3].Node != "d" {
		t.Fatalf("rising lineage: got %v", got)
	}
}
