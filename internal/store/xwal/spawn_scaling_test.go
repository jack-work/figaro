package xwal

import (
	"path/filepath"
	"testing"
)

// A spawn must not rebuild the index. It used to do so twice, each one a walk
// of every node directory reading a .Trunk marker, which is why minting aria
// 400 was a function of arias 1 through 399.
//
// This counts rebuilds rather than timing them on purpose. The cost is
// filesystem syscalls, so a wall-clock assertion passes on a fast local disk
// and only fails on the machine that reported the problem. The invariant is
// "does not re-read the tree", and that is observable directly.
func TestSpawnWalksTheTreeOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.CreateStump("s"); err != nil {
		t.Fatal(err)
	}

	base := f.idx.rebuilds.Load()
	for range 10 {
		if _, err := f.SpawnUnderStump("s"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.idx.rebuilds.Load() - base; got != 0 {
		t.Fatalf("10 spawns triggered %d tree walks, want 0", got)
	}

	// And the index still answers correctly: every trunk has a live head.
	for _, id := range f.idx.LiveTrunks() {
		key, ok := f.idx.Head(id)
		if !ok {
			t.Fatalf("trunk %s has no head after incremental spawn", id)
		}
		if n, ok := f.idx.Node(key); !ok || n.Trunk != id {
			t.Fatalf("head %s of trunk %s is wrong: %+v", key, id, n)
		}
	}
}
