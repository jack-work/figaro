package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// On a store snapshot, the parallel first listing labels every aria as
// labelling them one at a time does.
//
//	FIGARO_WARM_SNAPSHOT=/var/tmp/perf/startup-metrics/stores/big/arias go test ./internal/store -run Snapshot -v
func TestListedLabelsMatchOneByOneLabellingOnASnapshot(t *testing.T) {
	src := os.Getenv("FIGARO_WARM_SNAPSHOT")
	if src == "" {
		t.Skip("set FIGARO_WARM_SNAPSHOT to an arias directory (reflink-copied twice, never opened in place)")
	}
	open := func(name string) *XwalBackend {
		dir, err := os.MkdirTemp("/var/tmp", "figaro-labels-"+name+"-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		root := filepath.Join(dir, "arias")
		if out, err := exec.Command("cp", "-a", "--reflink=auto", src, root).CombinedOutput(); err != nil {
			t.Fatalf("copy snapshot: %v\n%s", err, out)
		}
		b, err := NewXwalBackend(root, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}
	got := listedLabels(open("parallel"))
	want := serialLabels(open("serial"))
	if len(want) < 1000 || len(got) != len(want) {
		t.Fatalf("listed %d, labelled %d", len(got), len(want))
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("%s listed as %v, labelled one by one as %v", id, got[id], w)
		}
	}
	t.Logf("%d arias labelled alike", len(want))
}
