package xwal

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #44: a reducible channel whose folded state outgrows the segment
// size used to fill every fresh segment with header alone, so the next patch
// failed at flush and every later create or fork on the store was refused.
func TestReducibleStateLargerThanSegmentKeepsStoreWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedTrunk(t, dir)
	value := strings.Repeat("v", 600)
	const patches = 40
	for i := 0; i < patches; i++ {
		_, lt, err := f.Append(root, 0, []byte(fmt.Sprintf(`"turn %d"`, i)), nil)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		patch := fmt.Sprintf(`{"set":{"k%02d":%q}}`, i, value)
		if _, err := f.AppendChannel(root, "chalkboard", lt, []byte(patch), nil); err != nil {
			t.Fatalf("chalkboard patch %d: %v", i, err)
		}
		if _, err := f.SpawnUnderStump("s"); err != nil {
			t.Fatalf("spawn after patch %d (state ~%d B, segment 4096 B): %v", i, (i+1)*620, err)
		}
	}
	if err := f.syncHot(); err != nil {
		t.Fatalf("hot flush: %v", err)
	}
	f.Close()

	g, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, g)
	x, err := g.Head(root)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	st, err := x.StateAt("chalkboard", chalkLast(t, x))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(st, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != patches || m["k00"] != value || m[fmt.Sprintf("k%02d", patches-1)] != value {
		t.Fatalf("state after reopen has %d keys, want %d", len(m), patches)
	}
}
