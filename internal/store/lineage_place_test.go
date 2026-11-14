package store

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// An interior fork cut below its parent's own records is born under the
// ancestor that owns the cut, then placed under the parent it was asked of
// (the angelus fork handler's place). Every range read of the child must
// still return the child's own records, not the parent's ancestors' suffix.
func TestForkPlacedUnderAParentAboveItsCutReadsItsOwnRecords(t *testing.T) {
	be, a, aLog := newConversation(t)
	for i := 0; i < 10; i++ {
		put(t, aLog, ask(uint64(i+1), fmt.Sprintf("a%d", i)))
	}
	b, _, err := be.ForkWith(a, 0, birth("b"))
	if err != nil {
		t.Fatal(err)
	}
	bLog, err := be.OpenFigIR(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		put(t, bLog, ask(uint64(20+i), fmt.Sprintf("b%d", i)))
	}
	c, _, err := be.ForkWith(b, 4, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Store().move(c, b); err != nil {
		t.Fatal(err)
	}
	cLog, err := be.OpenFigIR(c)
	if err != nil {
		t.Fatal(err)
	}
	var own []uint64
	for i := 0; i < 5; i++ {
		own = append(own, put(t, cLog, ask(uint64(30+i), fmt.Sprintf("c%d", i))))
	}

	text := func(e Entry[message.Message]) string {
		if len(e.Payload.Content) == 0 {
			return ""
		}
		return e.Payload.Content[0].Text
	}
	want := map[uint64]string{}
	for i, lt := range own {
		want[lt] = fmt.Sprintf("c%d", i)
	}
	for lt, w := range want {
		if e, ok := cLog.Lookup(lt); !ok || text(e) != w {
			t.Errorf("Lookup(%d) = %q, want %q", lt, text(e), w)
		}
	}
	scanned := map[uint64]string{}
	ScanAll(cLog, func(e Entry[message.Message]) bool {
		scanned[e.LT] = text(e)
		return true
	})
	tail, _ := TailAfter(cLog, own[0]-1)
	after := map[uint64]string{}
	for _, e := range tail {
		after[e.LT] = text(e)
	}
	for lt, w := range want {
		if scanned[lt] != w {
			t.Errorf("Scan at %d = %q, want %q (lineage %v)", lt, scanned[lt], w, be.Store().Lineage(c))
		}
		if after[lt] != w {
			t.Errorf("TailAfter at %d = %q, want %q", lt, after[lt], w)
		}
	}
}

// The read lineage keeps an ancestor only while its base is below the one
// beneath it: equal and higher bases own nothing the leaf can see.
func TestServingLineageDropsAncestorsAtOrAboveTheBound(t *testing.T) {
	links := []Link{{Node: "r"}, {Node: "a", BaseLT: 17}, {Node: "b", BaseLT: 21}, {Node: "c", BaseLT: 6}, {Node: "d", BaseLT: 6}, {Node: "e", BaseLT: 5}}
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
	rising := []Link{{Node: "r"}, {Node: "a", BaseLT: 3}, {Node: "b", BaseLT: 9}, {Node: "c", BaseLT: 9}, {Node: "d", BaseLT: 12}}
	if got := servingLineage(rising); len(got) != 4 || got[1].Node != "a" || got[2].Node != "c" || got[3].Node != "d" {
		t.Fatalf("rising lineage: got %v", got)
	}
}
