package tree

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// EVICTION VISITS EVERY RESIDENT RUN, and a full sweep therefore costs
// O(R^2). This is a MEASUREMENT, not a fix: the cure is an eviction index
// keyed by effective epoch, and adding a data structure is Gluck's call.
//
// The instrument is the Recency hook rather than new counters in the
// production path: it is already called exactly once per candidate run per
// scan, so counting its calls counts the scan without changing what is
// scanned. Returning 0 leaves effEpoch's answer alone -- it takes the max of
// the run's epoch and the oracle -- so the eviction ORDER is unchanged by
// measuring it.
func TestEvictionVisitsEveryResidentRun(t *testing.T) {
	for _, runs := range []int{16, 64, 256} {
		t.Run(fmt.Sprint(runs), func(t *testing.T) {
			var calls int
			var mu sync.Mutex
			// One unit is 1024 bytes of payload; runChunk is 64, so a Range of
			// 64 keys is exactly one run.
			b := NewBudget(0)
			c := newCache(b, &calls, &mu)
			lineage := []Ref{{Node: "p"}}
			for i := 0; i < runs; i++ {
				lo := uint64(i * runChunk)
				if _, err := c.Range(lineage, lo, lo+runChunk); err != nil {
					t.Fatal(err)
				}
			}
			// Runs are cut by BYTES now (runTargetBytes), so a 64-unit ask of
			// 1 KiB units becomes two runs. The scan's cost is per RUN, so the
			// fixture counts what it actually built.
			residentRuns := len(c.runs("p"))
			if residentRuns < runs {
				t.Fatalf("fixture: %d runs resident, want at least %d", residentRuns, runs)
			}

			var visits atomic.Int64
			c.Recency = func(Coord) int64 { visits.Add(1); return 0 }

			// One eviction: set the limit just under what is resident and
			// charge nothing, so the pass frees exactly one run.
			resident, _, _ := b.Stats()
			b.SetLimit(resident - 1)
			b.charge(0)
			// Eviction runs on the sweeper now: ask, then wait for it.
			b.Settle(2 * time.Second)

			if _, _, ev := b.Stats(); ev != 1 {
				t.Fatalf("want exactly one eviction, got %d", ev)
			}
			t.Logf("R=%d resident runs -> %d run visits for ONE eviction", residentRuns, visits.Load())
			if visits.Load() < int64(residentRuns) {
				t.Fatalf("R=%d: %d visits; the scan is expected to be linear in R "+
					"and this instrument says it is not -- check the instrument before the claim",
					residentRuns, visits.Load())
			}
		})
	}
}

// The sweep is the compounding case: TrimIdle re-scans from the top for EVERY
// run it drops. Recorded as a number rather than an assertion about the cure.
func TestSweepRescansPerDroppedRun(t *testing.T) {
	const runs = 64
	var calls int
	var mu sync.Mutex
	b := NewBudget(0)
	c := newCache(b, &calls, &mu)
	lineage := []Ref{{Node: "p"}}
	for i := 0; i < runs; i++ {
		lo := uint64(i * runChunk)
		if _, err := c.Range(lineage, lo, lo+runChunk); err != nil {
			t.Fatal(err)
		}
	}

	var visits atomic.Int64
	c.Recency = func(Coord) int64 { visits.Add(1); return 0 }

	// runs-1, not runs: TrimIdle advances the epoch and drops what is OLDER
	// than the cutoff, and the newest run was stamped at the epoch the sweep
	// starts from. Written as `runs` first, red at 63, and the survivor is the
	// policy rather than a leak.
	before := len(c.runs("p"))
	dropped, _ := b.TrimIdle(0)
	if dropped != before-1 {
		t.Fatalf("swept %d runs, want %d", dropped, before-1)
	}
	t.Logf("R=%d: a full sweep dropped %d runs in %d run visits (~R^2/2 = %d)",
		before, dropped, visits.Load(), before*before/2)
}
