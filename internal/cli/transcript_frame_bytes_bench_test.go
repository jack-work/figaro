package cli

import (
	"fmt"

	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The wire — how many bytes a frame actually costs the terminal. On a local
// terminal this is nearly free; over ssh or through tmux it is the whole
// latency budget, which is why it deserves its own number alongside ns/op.
//
// E and B arrived with a countingWriter each. B's (transcript_paint_bench_test
// .go) won the merge: identical job, but atomic, which its coalescing tests
// need because frames also arrive from the pacer's timer goroutine. Likewise
// countingTranscript is now a thin alias for B's heavyTranscriptOn.
func countingTranscript(b *testing.B, messages, outputLines int) (*transcript, *countingWriter) {
	b.Helper()
	cw := &countingWriter{}
	return heavyTranscriptOn(b, cw, messages, outputLines), cw
}

// BenchmarkTranscriptFrameBytes reports the bytes written per painted frame for
// a scroll step, which dirties every row of the viewport.
//
//	B/frame     bytes handed to the terminal per rendered frame
//	writes      Write calls per frame (the painter buffers, so this is 1)
//
// Run with -benchtime 200x; ns/op is the scroll benchmarks' job.
func BenchmarkTranscriptFrameBytes(b *testing.B) {
	for _, outputLines := range []int{20, 200} {
		b.Run(fmt.Sprintf("out%d", outputLines), func(b *testing.B) {
			tr, cw := countingTranscript(b, 200, outputLines)
			tr.scrollBy(-1)
			cw.reset()
			b.ResetTimer()
			for i := range b.N {
				if i%2 == 0 {
					tr.scrollBy(-1)
				} else {
					tr.scrollBy(1)
				}
			}
			b.StopTimer()
			if b.N > 0 {
				b.ReportMetric(float64(cw.bytes.Load())/float64(b.N), "B/frame")
				b.ReportMetric(float64(cw.writes.Load())/float64(b.N), "writes")
			}
		})
	}
}

// BenchmarkTranscriptEnterBytes covers the other shape: the first full paint,
// where every row of the screen is new.
func BenchmarkTranscriptEnterBytes(b *testing.B) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, 60)
	for i := range committed {
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, 20)}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	cw := &countingWriter{}
	b.ResetTimer()
	for range b.N {
		tr := newTranscript(cw, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
		tr.enter()
		tr.leave()
	}
	b.StopTimer()
	if b.N > 0 {
		b.ReportMetric(float64(cw.bytes.Load())/float64(b.N), "B/enter")
	}
}
