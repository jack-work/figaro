package xwal

import (
	"path/filepath"
	"testing"
)

// A closed Trunks gives its lineages up, so the next peer adopts nothing.
func TestClosingTrunksReleasesItsLineages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	builder, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	ids := spawnUnderRoot(t, builder, 3)
	root := builder.registryRoot
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}
	state := rootTopologyStateFor(root)
	checked := 0
	for _, id := range ids {
		state.mu.Lock()
		lineage := state.lineages[string(id)]
		state.mu.Unlock()
		if lineage == nil {
			continue
		}
		lineage.mu.Lock()
		owner := lineage.owner
		lineage.mu.Unlock()
		checked++
		if owner != nil {
			t.Fatalf("lineage %s still owned after its Trunks closed", id)
		}
	}
	if checked == 0 {
		t.Fatal("no lineage was recorded for the spawned trunks")
	}
}
