package store

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jack-work/figwal/xwal"
)

// Probes behind the mint path (`figaro new` -> angelus.create ->
// XwalStore.CreateConversation -> xwal Trunks.SpawnUnderStump). They print
// timings rather than asserting, because the numbers that matter are the
// SHAPE of the curve, not a threshold: each isolates one scaling term.
//
//	TestCreateFloor          fixed per-mint cost      (0 siblings, tiny forest)
//	TestCreateVsSiblings     O(siblings x channels)   (preflight rescan)
//	TestCreateVsForestSize   O(total forest nodes)    (rebuild walk)
//	TestForestWalkCost       one rebuild, real tree   (needs FIGARO_PERF_ROOT)
//
// Run them with -v; see RECOMMENDATION-UNDERSTUMP.md for measured output.

// perfRoot is deliberately NOT tb.TempDir(): figwal holds a .lock handle
// open, so the harness cleanup fails the run before results print.
func perfRoot(tb testing.TB) string {
	d, err := os.MkdirTemp("", "figperf")
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

func perfPatch() map[string]string {
	return map[string]string{"system.model": "m", "system.credo": "be terse"}
}

func bandMeans(samples []time.Duration, bands [][2]int, label func(lo, hi int) string) {
	for _, b := range bands {
		var sum time.Duration
		for _, d := range samples[b[0]:b[1]] {
			sum += d
		}
		fmt.Printf("%s: mean %v\n", label(b[0], b[1]), sum/time.Duration(b[1]-b[0]))
	}
}

// BenchmarkCreateConversation measures the pure mint path: SpawnUnderStump
// on an existing loadout stump, no turn, no LLM. Profile it with
// -cpuprofile to see the syscall breakdown.
func BenchmarkCreateConversation(b *testing.B) {
	be, err := NewXwalBackend(perfRoot(b), 0)
	if err != nil {
		b.Fatal(err)
	}
	lid, err := be.CreateLoadout("perf", patchSet(perfPatch()))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := be.CreateConversation(lid); err != nil {
			b.Fatal(err)
		}
	}
}

// TestCreateFloor: every mint goes under a FRESH stump, so the forest stays
// small and the stump has no siblings. This is the irreducible cost —
// fork plan, per-channel rehome, markers, fsyncs.
func TestCreateFloor(t *testing.T) {
	be, err := NewXwalBackend(perfRoot(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	var sum time.Duration
	for i := 0; i < n; i++ {
		lid, err := be.CreateLoadout(fmt.Sprintf("perf%d", i), patchSet(perfPatch()))
		if err != nil {
			t.Fatal(err)
		}
		s := time.Now()
		if _, err := be.CreateConversation(lid); err != nil {
			t.Fatal(err)
		}
		sum += time.Since(s)
	}
	fmt.Printf("create floor (0 siblings, small forest): mean %v\n", sum/n)
}

// TestCreateVsSiblings: one stump, fan-out grows. Isolates the
// forkTopologyStructurallyComplete rescan, which is O(siblings x channels)
// and runs on every mint regardless of the preflight cache.
func TestCreateVsSiblings(t *testing.T) {
	be, err := NewXwalBackend(perfRoot(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	lid, err := be.CreateLoadout("perf", patchSet(perfPatch()))
	if err != nil {
		t.Fatal(err)
	}
	const n = 200
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		s := time.Now()
		if _, err := be.CreateConversation(lid); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(s))
	}
	bandMeans(samples, [][2]int{{0, 10}, {10, 25}, {25, 50}, {50, 100}, {100, 150}, {150, 200}},
		func(lo, hi int) string { return fmt.Sprintf("siblings %3d-%3d", lo, hi) })
}

// TestCreateVsForestSize: siblings stay at 0 (fresh stump every time) while
// the total node count grows. Isolates the Trunks.rebuild() full-forest
// walk, which a single spawn performs twice.
func TestCreateVsForestSize(t *testing.T) {
	be, err := NewXwalBackend(perfRoot(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	const n = 150
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		lid, err := be.CreateLoadout(fmt.Sprintf("perf%d", i), patchSet(perfPatch()))
		if err != nil {
			t.Fatal(err)
		}
		s := time.Now()
		if _, err := be.CreateConversation(lid); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(s))
	}
	bandMeans(samples, [][2]int{{0, 25}, {25, 50}, {50, 75}, {75, 100}, {100, 125}, {125, 150}},
		func(lo, hi int) string { return fmt.Sprintf("forest nodes ~%3d-%3d", lo*2, hi*2) })
}

// TestForestWalkCost times cold open and one full topology rebuild against
// a real-sized aria forest. Point FIGARO_PERF_ROOT at a COPY of an arias
// tree — never a live one, since a second daemon must not open it.
func TestForestWalkCost(t *testing.T) {
	root := os.Getenv("FIGARO_PERF_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PERF_ROOT to a COPY of an arias tree")
	}
	t0 := time.Now()
	st, err := xwal.OpenStore(root, storeOptions(0))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("cold OpenStore: %v\n", time.Since(t0))
	for i := 0; i < 5; i++ {
		s := time.Now()
		if err := st.Refresh(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("Refresh (topology mutation + ONE full-forest walk): %v\n", time.Since(s))
	}
	fmt.Printf("stumps: %d\n", len(st.Stumps()))
}
