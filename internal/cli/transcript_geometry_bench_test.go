package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
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

// 300 is retained deliberately: transcriptMinPageSize floors a page at 6
// messages, so on a heavy aria every budget below ~800 rows produces the same
// window as 600 and the sweep should show that rather than hide it.
var geometryBudgets = []int{300, 600, 1200, 1800, 2400, 4800}

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
				b.ReportMetric(float64(tr.index.total), "windowrows")
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
				var fetches, refetches, evictions, keys, renders, window, peak, peakRows int
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
					window += h.tr.index.total
					peak += h.peakBytes
					peakRows += h.peakRows
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
				// The memory half of the tradeoff, which is the half that still
				// discriminates once the frame is O(viewport): peak RETAINED row
				// bytes (window + payload LRU), not per-op churn.
				b.ReportMetric(float64(peak)/n/1024, "peak-retained-KB")
				b.ReportMetric(float64(peakRows)/n, "peak-retained-rows")
			})
		})
	}
}

// BenchmarkTranscriptGeometryEnter is the cold-start side of the budget. The
// tail window converges on one page's worth of rows (transcriptWindowRows /
// transcriptPageLimit), and entering the pager has to render every one of them
// once. This is the cost that grows with the budget on the MERGED stack, where
// steady-state frame cost no longer does: so it belongs in the sweep that
// picks the number.
func BenchmarkTranscriptGeometryEnter(b *testing.B) {
	for _, rows := range geometryBudgets {
		b.Run(fmt.Sprintf("rows%d", rows), func(b *testing.B) {
			withWindowRows(rows, func() {
				var window int
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					tr, _ := heavyTranscript(b, 200, 60)
					window += tr.index.total
				}
				b.StopTimer()
				b.ReportMetric(float64(window)/float64(max(b.N, 1)), "tailrows")
			})
		})
	}
}

// BenchmarkTranscriptGeometryFollow is the live-tail side of the budget. Axis A
// left exactly one O(retained rows) step on the frame path: rebuildLineLT,
// which refills the LT-per-line map whenever the index shape moves, and a
// streaming open message moves it on every single frame. So unlike a scroll
// frame, a follow frame IS sensitive to the window size, and the sweep has to
// say by how much before the budget is raised.
func BenchmarkTranscriptGeometryFollow(b *testing.B) {
	for _, rows := range geometryBudgets {
		b.Run(fmt.Sprintf("rows%d", rows), func(b *testing.B) {
			withWindowRows(rows, func() {
				tr, client := heavyTranscript(b, 200, 60)
				client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
					"type": "prose", "markdown": "streaming"}}}}}}}})
				tr.render()
				b.ReportMetric(float64(tr.index.total), "windowrows")
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
						"type": "prose", "markdown": fmt.Sprintf("streaming token %d", i)}}}}}}}})
					tr.render()
				}
			})
		})
	}
}

// TestTranscriptGeometryDepthReport asks the question the merged stack forces
// and D's solo sweep could not: now that frame cost is flat in the window size,
// the only remaining benefit of a bigger window is less paging churn: so is the
// churn threshold a property of the BUDGET, or of how far the user scrolls?
//
// It sweeps budget x journey depth and prints fetches / refetched messages /
// node re-renders / peak retained bytes. A report, not an assertion (the
// numbers are machine- and fixture-dependent); the conclusion it supports is
// written up in docs/transcript-paging.md. Enable with
// FIGARO_PAGING_REPORT=1 go test ./internal/cli/ -run GeometryDepth -v.
func TestTranscriptGeometryDepthReport(t *testing.T) {
	if os.Getenv("FIGARO_PAGING_REPORT") == "" {
		t.Skip("set FIGARO_PAGING_REPORT=1 for the geometry depth report")
	}
	for _, depth := range []int{60, 120, 240} {
		for _, rows := range geometryBudgets {
			withWindowRows(rows, func() {
				h := newPagingHarness(600, 60, 100, 40)
				h.journey(depth)
				t.Logf("depth=%-4d budget=%-5d window=%-5d fetches=%-3d refetched=%-3d renders=%-4d peak=%d KB",
					depth, rows, h.tr.index.total, h.fetches, h.refetches,
					h.view.render, h.peakBytes/1024)
			})
		}
	}
}
