package aria

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func benchmarkServer(n int) *Server {
	s := NewServer()
	for i := 1; i <= n; i++ {
		s.Commit(Turn{
			ID:     uint64(i),
			Sealed: true,
			Nodes: []livedoc.Node{{
				Type:     livedoc.NodeProse,
				Role:     livedoc.RoleOutput,
				Markdown: "synthetic history",
			}},
		})
	}
	return s
}

func BenchmarkReadRecentLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			s := benchmarkServer(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page := s.Read(Anchor{Turn: uint64(n - 30)}, 1<<16)
				runtime.KeepAlive(page)
			}
		})
	}
}

func BenchmarkReadBeforeLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			s := benchmarkServer(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page := s.ReadBefore(Anchor{Turn: uint64(n + 1)}, 1<<16)
				runtime.KeepAlive(page)
			}
		})
	}
}
