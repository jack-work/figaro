package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Baseline for the forest re-seat. cachedLog serves reads from an
// atomic.Pointer view and takes no lock; the re-seat must not reintroduce one.
func benchEntry(lt uint64) Entry[string] {
	return Entry[string]{FigaroLT: lt, LT: lt, Payload: fmt.Sprintf("row-%d-%s", lt, string(make([]byte, 256)))}
}

func benchLog(n int) *cachedLog[string] {
	inner := NewMemLog[string]()
	for i := 1; i <= n; i++ {
		inner.Append(Entry[string]{Payload: benchEntry(uint64(i)).Payload})
	}
	return newWindowedLog[string](inner, 0, 1<<20, 1, func(e Entry[string]) int { return len(e.Payload) + 48 })
}

// The read the atomic.Pointer exists for: many readers against a live writer.
// "34 acquisitions on the hot read path, every one of which waited behind an
// append" is what this measures the absence of.
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
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = c.ReadFrom(1500, 64)
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
