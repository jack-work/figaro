package xwal

import (
	"os"
	"path/filepath"
	"testing"
)

// The point of the merge: one file per node, not two.
func TestNodeMarkerIsOneFilePerNode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"t"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.ForkTail(trunk); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "ir"))
	nodes, markers := 0, 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		nodes++
		sub, _ := os.ReadDir(filepath.Join(dir, "ir", e.Name()))
		for _, s := range sub {
			switch s.Name() {
			case ".node":
				markers++
			case ".from", ".trunk":
				t.Errorf("%s still writes a legacy marker %s", e.Name(), s.Name())
			}
		}
	}
	if nodes == 0 || markers != nodes {
		t.Fatalf("%d nodes, %d markers: want one each", nodes, markers)
	}
	t.Logf("%d nodes, %d identity files (was %d)", nodes, markers, nodes*2)
}
