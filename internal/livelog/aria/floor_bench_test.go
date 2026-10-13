package aria

import (
	"encoding/json"
	"runtime"
	"testing"
)

// A floored read after a fork hop should cost neither the bytes nor the
// composition of the prefix the client already holds.
func BenchmarkReadBeforeFloor(b *testing.B) {
	const turnCount = 2000
	turns := floorTurns(turnCount)
	cases := []struct {
		name  string
		floor Anchor
	}{
		{"unfloored", Anchor{}},
		{"floor=tail-20", Anchor{Turn: turnCount - 20}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			s, recomposed := floorServer(b, turns, 64<<10)
			// The first read is the cold one: the dormant-aria hop.
			page := s.ReadBefore(Anchor{}, c.floor, 1<<20)
			cold := *recomposed
			wire, err := json.Marshal(page)
			if err != nil {
				b.Fatal(err)
			}
			*recomposed = 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := s.ReadBefore(Anchor{}, c.floor, 1<<20)
				runtime.KeepAlive(p)
			}
			b.StopTimer()
			b.ReportMetric(float64(len(wire)), "page_bytes")
			b.ReportMetric(float64(len(page.Parts)), "parts")
			b.ReportMetric(float64(cold), "cold_recomposes")
			b.ReportMetric(float64(*recomposed)/float64(b.N), "hot_recomposes/op")
		})
	}
}
