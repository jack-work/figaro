package cli

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// Paint-bandwidth rig (axis B).
//
// The shared scroll rig writes to io.Discard, which makes the terminal look
// free. On a real tty — and especially over ssh or a tmux pipe — the bytes the
// pager pushes per scroll step are the thing you feel. `paint` diffs against
// t.prev, but a one-line scroll changes EVERY body row, so the old full-diff
// path re-transmits the whole screen every step.
//
// These benchmarks report two metrics the shared rig cannot:
//
//	B/frame      bytes written per painted frame
//	frames/op    how many frames one logical gesture produces
//
// Both are "lower is better" and neither shows up in ns/op.
// ---------------------------------------------------------------------------

// countingWriter is a sink that records volume rather than content: how many
// bytes were pushed and how many Write calls it took (one per painted frame).
type countingWriter struct {
	bytes  atomic.Int64
	writes atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.bytes.Add(int64(len(p)))
	c.writes.Add(1)
	return len(p), nil
}

func (c *countingWriter) reset() {
	c.bytes.Store(0)
	c.writes.Store(0)
}

// heavyTranscriptOn is heavyTranscript with a caller-supplied sink, so paint
// volume can be measured. Kept separate from the shared rig on purpose: the
// shared file is the cross-axis yardstick and must not move.
func heavyTranscriptOn(tb testing.TB, out io.Writer, messages, outputLines int) *transcript {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, messages)
	for i := range committed {
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, outputLines)}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	tr := newTranscript(out, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr
}

// BenchmarkTranscriptPaintBytes is the headline bandwidth number: bytes and
// terminal writes per one-line scroll step, at a realistic 100x40 viewport.
func BenchmarkTranscriptPaintBytes(b *testing.B) {
	for _, dir := range []struct {
		name  string
		delta int
	}{{"up", -1}, {"down", 1}} {
		b.Run(dir.name, func(b *testing.B) {
			var w countingWriter
			tr := heavyTranscriptOn(b, &w, 200, 200)
			tr.scrollBy(-200) // park mid-history so both directions have room
			w.reset()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr.scrollBy(dir.delta)
			}
			b.StopTimer()
			b.ReportMetric(float64(w.bytes.Load())/float64(b.N), "B/step")
			b.ReportMetric(float64(w.writes.Load())/float64(b.N), "frames/step")
		})
	}
}

// BenchmarkTranscriptPaintHalfPage measures a half-page jump (u/d), where the
// shifted region is smaller and the fallback path matters more.
func BenchmarkTranscriptPaintHalfPage(b *testing.B) {
	var w countingWriter
	tr := heavyTranscriptOn(b, &w, 200, 200)
	tr.scrollBy(-400)
	w.reset()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%2 == 0 {
			tr.scrollBy(-20)
		} else {
			tr.scrollBy(20)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(w.bytes.Load())/float64(b.N), "B/step")
}

// BenchmarkTranscriptPaintTick measures a spinner tick: the content is
// identical except for one animating glyph, so an ideal frame is a handful of
// bytes.
func BenchmarkTranscriptPaintTick(b *testing.B) {
	var w countingWriter
	tr := heavyTranscriptOn(b, &w, 200, 200)
	tr.scrollBy(-200)
	w.reset()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.tick++
		tr.render()
	}
	b.StopTimer()
	b.ReportMetric(float64(w.bytes.Load())/float64(b.N), "B/frame")
}

// BenchmarkTranscriptPaintOnly isolates paint from row materialization: the
// same two screens are painted alternately, so the only cost is the diff and
// the escape stream. (t.prev is by definition the last painted screen, so no
// extra hook is needed to capture one.)
func BenchmarkTranscriptPaintOnly(b *testing.B) {
	var w countingWriter
	tr := heavyTranscriptOn(b, &w, 200, 200)
	tr.scrollBy(-200)
	a := clonedScreen(tr.prev)
	tr.scrollBy(-1)
	c := clonedScreen(tr.prev)
	w.reset()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%2 == 0 {
			tr.paint(clonedScreen(a))
		} else {
			tr.paint(clonedScreen(c))
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(w.bytes.Load())/float64(b.N), "B/frame")
}

func clonedScreen(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}
