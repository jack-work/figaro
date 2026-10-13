package xwal

import (
	"fmt"
	"sync"
	"testing"
)

// The counter registry grows by successor, and a successor built from a
// stale copy silently drops a node's counter. The lookup is lock-free and
// only growth is serialized; this pins the half that still needs the lock.
func TestLastTSRegistryGrowthKeepsEveryCounter(t *testing.T) {
	r := newLastTSRegistry()

	const n = 32
	var wg sync.WaitGroup
	got := make([]*nodeTS, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = r.counter(fmt.Sprintf("node-%d", i))
		}(i)
	}
	wg.Wait()

	m := *r.m.Load()
	if len(m) != n {
		t.Fatalf("created %d counters, the registry holds %d: a successor overwrote a sibling", n, len(m))
	}
	for i := 0; i < n; i++ {
		if m[fmt.Sprintf("node-%d", i)] != got[i] {
			t.Fatalf("node-%d: the registry holds a different counter than the caller was handed", i)
		}
	}
}

// One node asked for concurrently must yield ONE counter, or two writers
// advance two different timestamps and the newest one is invisible.
func TestLastTSRegistryHandsOutOneCounterPerNode(t *testing.T) {
	r := newLastTSRegistry()
	const n = 32
	var wg sync.WaitGroup
	seen := make([]*nodeTS, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); seen[i] = r.counter("same") }(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if seen[i] != seen[0] {
			t.Fatalf("caller %d got a different counter for the same node", i)
		}
	}
}
