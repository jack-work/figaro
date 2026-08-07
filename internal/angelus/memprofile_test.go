package angelus

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// heapDelta reports live-heap growth across fn, holding the result alive so
// the GC cannot reclaim what we are trying to weigh. Two GCs before and
// after: the first sweeps, the second collects what the first's finalizers
// freed, which is the standard way to get a stable HeapAlloc reading.
func heapDelta(fn func() any) (bytes uint64, keep any) {
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	keep = fn()
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc < before.HeapAlloc {
		return 0, keep
	}
	return after.HeapAlloc - before.HeapAlloc, keep
}

// TestWhereTheMemoryGoes weighs each holder that a live aria pins. It
// PRINTS rather than asserts, and exists so a tuning decision cites a
// measurement instead of the 3.0 GB anecdote in the EvictIdle comment.
//
//	go test ./internal/angelus/ -run WhereTheMemory -v
//
// CAUTION: synthetic prose messages are all alike and small, which makes
// the composed UI look LARGER than the decoded IR (1.5x here). On real
// arias it is 0.2x — tool calls and large results inflate the IR far more
// than the projection. Use this for trends under a controlled shape; use
// TestRealAriaMemory (realaria_probe_test.go) for any claim about which
// holder dominates.
func TestWhereTheMemoryGoes(t *testing.T) {
	const n = 10_000

	backend, id := benchStore(t, n)

	// 1. The decoded IR: cachedLog materializes every row at construction.
	irBytes, irKeep := heapDelta(func() any {
		log, err := backend.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		return log.Read()
	})

	// 2. The composed UI IR, which is what an agent builds eagerly in
	// NewAgent and holds for its whole life.
	proj := uiir.New(nil)
	uiBytes, uiKeep := heapDelta(func() any {
		msgs := irKeep.([]store.Entry[message.Message])
		flat := make([]message.Message, 0, len(msgs))
		for _, e := range msgs {
			flat = append(flat, e.Payload)
		}
		srv := aria.NewServer()
		for _, tn := range proj.Turns(flat) {
			srv.Commit(tn)
		}
		return srv
	})

	// 3. The chalkboard: board plus every patch in version order.
	cbBytes, cbKeep := heapDelta(func() any {
		snap, err := backend.ChalkboardState(id)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	})

	onDisk := irOnDiskBytes(t, irKeep.([]store.Entry[message.Message]))
	t.Logf("aria of %d messages, %s of IR payload", n, human(onDisk))
	t.Logf("  decoded IR (cachedLog)   %10s   %4.1fx payload", human(irBytes), ratio(irBytes, onDisk))
	t.Logf("  composed UI (aria.Server)%10s   %4.1fx payload", human(uiBytes), ratio(uiBytes, onDisk))
	t.Logf("  chalkboard               %10s", human(cbBytes))
	t.Logf("  TOTAL resident per aria  %10s", human(irBytes+uiBytes+cbBytes))
	runtime.KeepAlive(uiKeep)
	runtime.KeepAlive(cbKeep)
}

// irOnDiskBytes is the encoded size of the payloads, as a denominator for
// the in-memory multiple.
func irOnDiskBytes(t *testing.T, rows []store.Entry[message.Message]) uint64 {
	t.Helper()
	var total uint64
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			total += uint64(len(c.Text))
		}
	}
	return total
}

func human(b uint64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func ratio(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
