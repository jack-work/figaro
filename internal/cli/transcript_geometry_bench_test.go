package cli

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
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
					"type": "prose", "markdown": "streaming"}}}}}}}}, aria.Notify)
				tr.render()
				b.ReportMetric(float64(tr.index.total), "windowrows")
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
						"type": "prose", "markdown": fmt.Sprintf("streaming token %d", i)}}}}}}}}, aria.Notify)
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
// written up in skills/figaro/contributing/notes/transcript-paging.md. Enable with
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

// BenchmarkTranscriptIdleIndex is the clock's own cost: what asking for a
// current line index costs on a frame where NOTHING about the conversation
// moved. The pager asks eleven times a second for the spinner alone, and
// before the index carried a stamp every one of those asks re-walked the
// retained window and recomposed the live message.
func BenchmarkTranscriptIdleIndex(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 60)
	tr.settle()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.settle()
	}
}

// BenchmarkTranscriptLiveTickFrame is the frame the goroutine dumps caught: a
// long question carrying a board's worth of form deltas, a tool spinning under
// it, and the clock asking for a frame eleven times a second. Only the live
// entry may be recomposed, and its adornment must not be rebuilt from scratch
// to decide whether to draw one glyph.
func BenchmarkTranscriptLiveTickFrame(b *testing.B) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	parts := make([]aria.TurnPart, 40)
	for i := range parts {
		parts[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true,
			Inquiry: heavyInquiry(i + 1), Nodes: heavyNodes(i+1, 20)}}
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)

	deltas := map[string]livedoc.FormDelta{}
	for i := range 30 {
		key := fmt.Sprintf("board.skills.key%02d", i)
		deltas[key] = livedoc.FormDelta{
			Form: "board", Kind: livedoc.FormBound, Event: livedoc.FormSet,
			Value: []byte(fmt.Sprintf("%q", "a value of the sort a chalkboard carries")),
		}
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 99, Inquiry: heavyInquiry(99), FormDeltas: deltas,
		Nodes: []livedoc.Node{{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning}},
	}}}}, aria.Notify)

	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "livetick", time.Unix(0, 0))
	tr.enter()
	tr.settle()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.tick++
		tr.settle()
	}
}
