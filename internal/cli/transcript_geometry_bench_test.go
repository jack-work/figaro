package cli

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Geometry sweep: what does the retained-window row budget actually buy?
//
// The window is the thing every frame re-materializes, and the thing that
// decides how often we must fetch. These two benchmarks measure both ends of
// that tradeoff across budgets, so transcriptWindowRows is a number with data
// behind it rather than a guess. Run:
//
//	go test ./internal/cli/ -run XXX -bench Geometry -benchtime 20x -benchmem
// ---------------------------------------------------------------------------

var geometryBudgets = []int{300, 600, 1200, 2400, 4800}

func withWindowRows(rows int, fn func()) {
	prev := transcriptWindowRows
	transcriptWindowRows = rows
	defer func() { transcriptWindowRows = prev }()
	fn()
}

// BenchmarkTranscriptGeometryFrame is the cost side: one scroll frame with a
// heavy retained window, per budget.
func BenchmarkTranscriptGeometryFrame(b *testing.B) {
	for _, rows := range geometryBudgets {
		b.Run(fmt.Sprintf("rows%d", rows), func(b *testing.B) {
			withWindowRows(rows, func() {
				tr, _ := heavyTranscript(b, 200, 60)
				tr.scrollBy(-1)
				b.ReportMetric(float64(len(tr.lineLT)), "windowrows")
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					if i%2 == 0 {
						tr.scrollBy(-1)
					} else {
						tr.scrollBy(1)
					}
				}
			})
		})
	}
}

// BenchmarkTranscriptGeometryJourney is the churn side: the same round trip
// through history at each budget, reporting fetches and re-renders. A smaller
// window is cheaper per frame but pages more often; this is where the two
// curves cross.
func BenchmarkTranscriptGeometryJourney(b *testing.B) {
	for _, rows := range geometryBudgets {
		b.Run(fmt.Sprintf("rows%d", rows), func(b *testing.B) {
			withWindowRows(rows, func() {
				var fetches, refetches, evictions, keys, renders, window int
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					b.StopTimer()
					h := newPagingHarness(600, 60, 100, 40)
					b.StartTimer()
					h.journey(120)
					b.StopTimer()
					fetches += h.fetches
					refetches += h.refetches
					evictions += h.evictions
					keys += h.keys
					renders += h.view.render
					window += len(h.tr.lineLT)
					b.StartTimer()
				}
				b.StopTimer()
				n := float64(max(b.N, 1))
				b.ReportMetric(float64(window)/n, "windowrows")
				b.ReportMetric(float64(fetches)/n, "fetches/op")
				b.ReportMetric(float64(refetches)/n, "refetched-msgs/op")
				b.ReportMetric(float64(evictions)/n, "evicted-msgs/op")
				b.ReportMetric(float64(renders)/n, "noderenders/op")
				b.ReportMetric(float64(keys)/n, "keys/op")
			})
		})
	}
}

// BenchmarkTranscriptGeometryLRU sweeps how many evicted pages keep their
// payload+rows. With rows-based (i.e. smaller) pages, a 3-page LRU no longer
// spans the turn-around, so the return trip refetches and re-renders.
func BenchmarkTranscriptGeometryLRU(b *testing.B) {
	for _, lru := range []int{3, 6, 12, 24} {
		b.Run(fmt.Sprintf("lru%d", lru), func(b *testing.B) {
			prev := transcriptPayloadLRULimit
			transcriptPayloadLRULimit = lru
			defer func() { transcriptPayloadLRULimit = prev }()
			var fetches, refetches, renders int
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				h := newPagingHarness(600, 60, 100, 40)
				b.StartTimer()
				h.journey(120)
				b.StopTimer()
				fetches += h.fetches
				refetches += h.refetches
				renders += h.view.render
				b.StartTimer()
			}
			b.StopTimer()
			n := float64(max(b.N, 1))
			b.ReportMetric(float64(fetches)/n, "fetches/op")
			b.ReportMetric(float64(refetches)/n, "refetched-msgs/op")
			b.ReportMetric(float64(renders)/n, "noderenders/op")
		})
	}
}
