package aria

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

func benchmarkServer(n int) *Server {
	s := NewServer()
	for i := 1; i <= n; i++ {
		s.Commit(Message{
			LT:   i,
			Role: "assistant",
			Nodes: []livedoc.Node{{
				Type:     livedoc.NodeProse,
				Markdown: "synthetic history",
			}},
		})
	}
	return s
}

func BenchmarkReadRecentLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			s := benchmarkServer(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page := s.Read(n - 30)
				runtime.KeepAlive(page)
			}
		})
	}
}

func BenchmarkReadBeforeLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			s := benchmarkServer(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page := s.ReadBefore(n+1, 30)
				runtime.KeepAlive(page)
			}
		})
	}
}
