package aria

import (
	"fmt"
	"runtime"
	"testing"
)

// RANGE ALGEBRA BENCHMARKS — insert/merge/Query at 100/1k/10k messages, so
// phase 2 has a baseline to regress against when the transcript starts reading
// from the store instead of from a flat list.

// benchStore builds a store holding n single-node messages, one per turn, with
// every turn's extent known — so they coalesce into ONE range, which is the
// degenerate case the doc promises for an aria nobody has jumped around in.
func benchStore(n int) *Store {
	s := NewStore()
	for i := 1; i <= n; i++ {
		s.SetTurnLen(uint64(i), 1)
		s.Insert(unit(i, 0))
	}
	return s
}

// benchStoreFragmented holds the same n messages as ~n/8 separate ranges: every
// eighth turn's extent is withheld, so a hole opens there. This is the shape
// after mid-history eviction or a scatter of jumps.
func benchStoreFragmented(n int) *Store {
	s := NewStore()
	for i := 1; i <= n; i++ {
		if i%8 != 0 {
			s.SetTurnLen(uint64(i), 1)
		}
		s.Insert(unit(i, 0))
	}
	return s
}

func BenchmarkStoreInsertAppend(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(benchStore(n))
			}
		})
	}
}

func BenchmarkStoreInsertFragmented(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(benchStoreFragmented(n))
			}
		})
	}
}

// BenchmarkStoreMergeCoalesce measures the cost of learning the extent that
// fuses two neighbouring ranges — the merge path proper.
func BenchmarkStoreMergeCoalesce(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s := benchStoreFragmented(n)
				b.StartTimer()
				for t := 8; t <= n; t += 8 {
					s.SetTurnLen(uint64(t), 1)
				}
				if len(s.ranges) != 1 {
					b.Fatalf("expected one range, got %d", len(s.ranges))
				}
			}
		})
	}
}

func BenchmarkStoreQueryWhole(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			s := benchStore(n)
			lo, hi := Anchor{Turn: 1}, Anchor{Turn: uint64(n)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(s.Query(lo, hi))
			}
		})
	}
}

// BenchmarkStoreQueryWindow is the shape a pager asks for: a screenful.
func BenchmarkStoreQueryWindow(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			s := benchStore(n)
			lo := Anchor{Turn: uint64(n) / 2}
			hi := Anchor{Turn: uint64(n)/2 + 40}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(s.Query(lo, hi))
			}
		})
	}
}

func BenchmarkStoreQueryFragmented(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			s := benchStoreFragmented(n)
			lo, hi := Anchor{Turn: 1}, Anchor{Turn: uint64(n)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(s.Query(lo, hi))
			}
		})
	}
}

func BenchmarkStoreEvictMiddle(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s := benchStore(n)
				b.StartTimer()
				s.Evict(Anchor{Turn: uint64(n) / 3}, Anchor{Turn: 2 * uint64(n) / 3})
			}
		})
	}
}
