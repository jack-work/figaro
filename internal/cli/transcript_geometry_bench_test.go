package cli

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Geometry sweep: both ends of the retained-window tradeoff across budgets, so
// transcriptWindowRows is a number with data behind it.
//
//	go test ./internal/cli/ -run XXX -bench Geometry -benchtime 20x -benchmem

// transcriptMinPageSize floors a page at 6 messages, so budgets below ~800
// rows produce the same window as 600.
var geometryBudgets = []int{300, 600, 1200, 1800, 2400, 4800}

func withWindowRows(rows int, fn func()) {
	prev := transcriptWindowRows
	transcriptWindowRows = rows
	defer func() { transcriptWindowRows = prev }()
	fn()
}

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
				b.ReportMetric(float64(peak)/n/1024, "peak-retained-KB")
				b.ReportMetric(float64(peakRows)/n, "peak-retained-rows")
			})
		})
	}
}

// Entering the pager renders one page's worth of rows, which grows with the budget.
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

// rebuildLineLT is O(retained rows) and a streaming open message triggers it
// every frame, so a follow frame is sensitive to the window size.
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

// A report, not an assertion: FIGARO_PAGING_REPORT=1 go test -run GeometryDepth -v.
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

// The pager asks for the index eleven times a second for the spinner alone.
func BenchmarkTranscriptIdleIndex(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 60)
	tr.settle()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.settle()
	}
}

// Only the live entry may be recomposed on a tick, and its adornment must
// not be rebuilt to decide whether to draw one glyph.
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
		key := fmt.Sprintf("bound.skills.key%02d", i)
		deltas[key] = livedoc.FormDelta{
			Form: "bound", Kind: livedoc.FormBound, Event: livedoc.FormSet,
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
