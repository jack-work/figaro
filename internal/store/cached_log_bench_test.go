package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Baseline for the tree re-seat. cachedLog serves reads from an
// atomic.Pointer view and takes no lock; the re-seat must not reintroduce one.
func benchEntry(lt uint64) Entry[string] {
	return Entry[string]{FigaroLT: lt, LT: lt, Payload: fmt.Sprintf("row-%d-%s", lt, string(make([]byte, 256)))}
}

func benchLog(n int) *cachedLog[string] {
	inner := NewMemLog[string]()
	for i := 1; i <= n; i++ {
		inner.Append(Entry[string]{Payload: benchEntry(uint64(i)).Payload})
	}
	return newWindowedLog[string](inner, 0, 1<<20, 1, 1, func(e Entry[string]) int { return len(e.Payload) + 48 })
}

// THE READ THE atomic.Pointer EXISTS FOR: many readers against a live writer,
// AT A COORDINATE THAT STAYS INSIDE THE PUBLISHED WINDOW.
//
// The coordinate is tail-relative and re-read every iteration, because the
// window MOVES. This benchmark previously read at a fixed LT 1500 while the
// writer appended in a tight loop; the writer trimmed past 1500 within
// milliseconds, so 98% of its iterations fell THROUGH the published view into
// the mutex-guarded inner log. It was cited as evidence that the hot read path
// takes no lock, and it was measuring the path that locks. Counted two ways:
// 196,717 of 200,000 reads below the window, and MemLog.ReadFrom holding
// 97.95% of 134s of mutex delay.
//
// The signature it produced was real -- faster with more readers -- but that
// is a THROUGHPUT curve, not a lock-freedom test: under RunParallel, ns/op is
// wall time over completed ops, so more cores lower it under contention too.
// A performance shape is consistent with a mechanism; it does not establish
// one. What establishes it is lockfree_probe_test.go, which holds the lock and
// asks whether the read still answers.
//
// Found by aria 3a9225b1, verified independently. See
// ~/notes/layered-cache-design.md, "CONDITION 1'S EVIDENCE WAS THE WRONG
// EVIDENCE".
func BenchmarkCachedLogReadFromParallel(b *testing.B) {
	c := benchLog(2000)
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			c.Append(Entry[string]{Payload: benchEntry(uint64(i)).Payload})
		}
	}()
	var below atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			v := c.load()
			if len(v.rows) == 0 {
				continue
			}
			// Read INSIDE the resident window: 64 rows back from its tail.
			from := v.rows[len(v.rows)-1].FigaroLT
			if n := len(v.rows); n > 64 {
				from = v.rows[n-64].FigaroLT
			} else {
				from = v.rows[0].FigaroLT
			}
			if c.load().belowWindow(from) {
				below.Add(1)
			}
			_ = c.ReadFrom(from, 64)
		}
	})
	b.StopTimer()
	stop.Store(true)
	wg.Wait()
	// THE VACUITY GUARD, and it is the whole point of this rewrite: if the
	// reads are not landing in the window, this benchmark is measuring the
	// inner log again and its number means the opposite of what it says.
	if n := below.Load(); n > int64(b.N)/100 {
		b.Fatalf("%d of %d reads fell BELOW the window (>1%%): this benchmark is measuring the "+
			"mutex-guarded inner log, not the published view it claims to measure", n, b.N)
	}
}

// The FALL-THROUGH path, kept and honestly named. A read below the resident
// window takes the inner log's lock; that is what a cold hop costs, it is real,
// and it deserves a benchmark that says so in its name rather than one
// pretending to measure the lock-free path.
func BenchmarkCachedLogReadBelowWindowParallel(b *testing.B) {
	c := benchLog(2000)
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			c.Append(Entry[string]{Payload: benchEntry(uint64(i)).Payload})
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = c.ReadFrom(1500, 64) // trimmed away almost immediately
		}
	})
	b.StopTimer()
	stop.Store(true)
	wg.Wait()
}

func BenchmarkCachedLogReadFromSerial(b *testing.B) {
	c := benchLog(2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.ReadFrom(1500, 64)
	}
}

func BenchmarkCachedLogAppend(b *testing.B) {
	c := benchLog(2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Append(Entry[string]{Payload: benchEntry(uint64(i)).Payload})
	}
}
