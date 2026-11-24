package store

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestEachInParallelVisitsEveryIndexOnce(t *testing.T) {
	for _, n := range []int{0, 1, 2, 1000} {
		var mu sync.Mutex
		seen := map[int]int{}
		eachInParallel(n, func(i int) {
			mu.Lock()
			seen[i]++
			mu.Unlock()
		})
		if len(seen) != n {
			t.Fatalf("n=%d: %d indices visited", n, len(seen))
		}
		for i, c := range seen {
			if i < 0 || i >= n || c != 1 {
				t.Fatalf("n=%d: index %d visited %d times", n, i, c)
			}
		}
	}
}

func serialLabels(b *XwalBackend) map[string][2]string {
	out := map[string][2]string{}
	for _, n := range b.unburied(b.store.Conversations()) {
		b.label(&n)
		out[n.ID] = [2]string{n.Outfit, n.Version}
	}
	return out
}

func listedLabels(b *XwalBackend) map[string][2]string {
	out := map[string][2]string{}
	for _, n := range b.Conversations() {
		out[n.ID] = [2]string{n.Outfit, n.Version}
	}
	return out
}

func TestListedLabelsMatchOneByOneLabelling(t *testing.T) {
	be := NewTestBackend(t)
	for o := range 3 {
		outfit, err := be.CreateOutfit(fmt.Sprintf("o%d", o), setPatch(map[string]string{"system.model": "m"}))
		if err != nil {
			t.Fatal(err)
		}
		for c := range 20 {
			if _, err := be.CreateConversation(outfit); err != nil {
				t.Fatal(err)
			}
			if _, _, err := be.ForkWith("", 0, setPatch(map[string]string{
				keyOutfitName: fmt.Sprintf("own%d", c), keyOutfitVer: fmt.Sprintf("v%d", c),
			})); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := listedLabels(be)
	want := serialLabels(be)
	if len(got) != 120 || len(want) != len(got) {
		t.Fatalf("listed %d, labelled %d, want 120 each", len(got), len(want))
	}
	own := 0
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("%s listed as %v, labelled one by one as %v", id, got[id], w)
		}
		if strings.HasPrefix(w[0], "own") {
			own++
		}
	}
	if own != 60 {
		t.Fatalf("%d arias carry their own label, want 60: the fixture lost the stumpless shape", own)
	}
}
