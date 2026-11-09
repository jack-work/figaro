package xwal

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func benchCowStore(b *testing.B) *Store {
	b.Helper()
	s, err := OpenStore(b.TempDir(), StoreOptions{Main: "main", SegmentSize: 4096, IdleUnload: -1})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func benchAria(b *testing.B, s *Store, records int) string {
	b.Helper()
	id, err := s.SpawnUnderRoot()
	if err != nil {
		b.Fatal(err)
	}
	for i := 1; i <= records; i++ {
		if _, err := s.Append(id, "main", 0, []byte(fmt.Sprintf(`{"m":%d}`, i)), nil); err != nil {
			b.Fatal(err)
		}
	}
	return id
}

func benchQuantiles(b *testing.B, lat []time.Duration, unit string) {
	if len(lat) == 0 {
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	at := func(p float64) time.Duration { return lat[int(float64(len(lat)-1)*p)] }
	b.ReportMetric(float64(at(0.50).Microseconds()), "p50-"+unit)
	b.ReportMetric(float64(at(0.99).Microseconds()), "p99-"+unit)
	b.ReportMetric(float64(lat[len(lat)-1].Microseconds()), "max-"+unit)
}

// One removal with nothing else open: the cost every reaped aria pays.
func BenchmarkRemoveIdle(b *testing.B) {
	s := benchCowStore(b)
	ids := make([]string, b.N)
	for i := range ids {
		ids[i] = benchAria(b, s, 8)
	}
	s.syncDirty()
	b.ResetTimer()
	for _, id := range ids {
		if err := s.Remove(id, true); err != nil {
			b.Fatal(err)
		}
	}
}

// The case the store-wide lock lost: another aria holds a head open (a
// request body streaming) while removals run. failed/op counts removals that
// gave up.
func BenchmarkRemoveBesideAnOpenHead(b *testing.B) {
	s := benchCowStore(b)
	other := benchAria(b, s, 8)
	ids := make([]string, b.N)
	for i := range ids {
		ids[i] = benchAria(b, s, 8)
	}
	s.syncDirty()
	held, err := s.Trunks.Head(other)
	if err != nil {
		b.Fatal(err)
	}
	defer held.Close()
	failed := 0
	lat := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for _, id := range ids {
		start := time.Now()
		if err := s.Remove(id, true); err != nil {
			failed++
		}
		lat = append(lat, time.Since(start))
	}
	b.StopTimer()
	b.ReportMetric(float64(failed)/float64(b.N), "failed/op")
	benchQuantiles(b, lat, "us")
}

// What a burst of deletes costs everyone else: spawn latency on unrelated
// arias while another goroutine removes 200 arias back to back.
func BenchmarkSpawnDuringDeleteBurst(b *testing.B) {
	for _, burst := range []bool{false, true} {
		name := "idle"
		if burst {
			name = "deleting"
		}
		b.Run(name, func(b *testing.B) {
			s := benchCowStore(b)
			doomed := make([]string, 200)
			for i := range doomed {
				doomed[i] = benchAria(b, s, 4)
			}
			s.syncDirty()
			var stop atomic.Bool
			var wg sync.WaitGroup
			if burst {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for _, id := range doomed {
						if stop.Load() {
							return
						}
						_ = s.Remove(id, true)
					}
				}()
			}
			lat := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				if _, err := s.SpawnUnderRoot(); err != nil {
					b.Fatal(err)
				}
				lat = append(lat, time.Since(start))
			}
			b.StopTimer()
			stop.Store(true)
			wg.Wait()
			benchQuantiles(b, lat, "us")
		})
	}
}

// Forking a live aria's tail, the shape of `figaro fork`, during the same burst.
func BenchmarkForkDuringDeleteBurst(b *testing.B) {
	for _, burst := range []bool{false, true} {
		name := "idle"
		if burst {
			name = "deleting"
		}
		b.Run(name, func(b *testing.B) {
			s := benchCowStore(b)
			parent := benchAria(b, s, 16)
			doomed := make([]string, 200)
			for i := range doomed {
				doomed[i] = benchAria(b, s, 4)
			}
			s.syncDirty()
			var stop atomic.Bool
			var wg sync.WaitGroup
			if burst {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for _, id := range doomed {
						if stop.Load() {
							return
						}
						_ = s.Remove(id, true)
					}
				}()
			}
			lat := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				if _, err := s.Trunks.ForkTail(parent); err != nil {
					b.Fatal(err)
				}
				lat = append(lat, time.Since(start))
			}
			b.StopTimer()
			stop.Store(true)
			wg.Wait()
			benchQuantiles(b, lat, "us")
		})
	}
}

// A translator cut on one aria while another aria's head is held open.
func BenchmarkCutBesideAnOpenHead(b *testing.B) {
	s := benchCowStore(b)
	other := benchAria(b, s, 8)
	victim := benchAria(b, s, 8)
	s.syncDirty()
	held, err := s.Trunks.Head(other)
	if err != nil {
		b.Fatal(err)
	}
	defer held.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for j := 1; j <= 4; j++ {
			if _, err := s.Append(victim, "notes", uint64(j), []byte(`{"n":1}`), nil); err != nil {
				b.Fatal(err)
			}
		}
		s.syncDirty()
		b.StartTimer()
		if err := s.TruncateFrom(victim, "notes", 1); err != nil {
			b.Fatal(err)
		}
	}
}
