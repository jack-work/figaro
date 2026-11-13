package xwal

import (
	"fmt"
	"slices"
	"testing"
)

// The journal answers only for a run of spawns of new trunks. Anything that
// can change an existing trunk's lineage is a barrier the caller must answer
// with a full walk.
func TestSpawnedSinceAnswersOnlyForSpawnsOfNewTrunks(t *testing.T) {
	x := newIndex(nil)
	x.SpawnFlat("", "n0", "t0", "conversation")
	v0 := x.Version()

	x.SpawnFlat("n0", "n1", "t1", "conversation")
	x.SpawnFlat("n0", "n2", "", "loadout")
	x.SpawnFlat("n1", "n3", "t2", "conversation")
	got, at, ok := x.SpawnedSince(v0)
	if !ok || at != x.Version() || !slices.Equal(got, []string{"t1", "t2"}) {
		t.Fatalf("after three spawns: %v at %d ok %v", got, at, ok)
	}
	if got, _, ok := x.SpawnedSince(x.Version()); !ok || len(got) != 0 {
		t.Fatalf("nothing since now: %v ok %v", got, ok)
	}

	barriers := map[string]func(){
		"reparent":  func() { x.Reparent("n3", "n0") },
		"drop":      func() { x.Drop([]string{"n3"}) },
		"head move": func() { x.SpawnFlat("n1", fmt.Sprint("m", x.Version()), "t1", "conversation") },
		"rebuild":   func() { _ = x.RebuildFrom(t.TempDir()) },
	}
	for name, mutate := range barriers {
		before := x.Version()
		mutate()
		if _, _, ok := x.SpawnedSince(before); ok {
			t.Fatalf("%s: the journal answered across a barrier", name)
		}
		after := x.Version()
		x.SpawnFlat("", fmt.Sprint("k", after), fmt.Sprint("tk", after), "conversation")
		if got, _, ok := x.SpawnedSince(after); !ok || len(got) != 1 {
			t.Fatalf("%s: a spawn after the barrier: %v ok %v", name, got, ok)
		}
	}
}

func TestSpawnedSinceRefusesPastTheJournal(t *testing.T) {
	x := newIndex(nil)
	start := x.Version()
	for i := 0; i <= spawnJournal; i++ {
		x.SpawnFlat("", fmt.Sprint("n", i), fmt.Sprint("t", i), "conversation")
	}
	if _, _, ok := x.SpawnedSince(start); ok {
		t.Fatal("answered for a version the journal no longer reaches")
	}
	if got, _, ok := x.SpawnedSince(start + 1); !ok || len(got) != spawnJournal {
		t.Fatalf("the oldest version still covered: %d trunks ok %v", len(got), ok)
	}
}
