package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func benchScreen(h int) []string {
	s := make([]string, h)
	for i := range s {
		s[i] = fmt.Sprintf("\x1b[2m%3d\x1b[0m  a row of transcript about as wide as a real one gets %d", i, i)
	}
	return s
}

func benchTranscript(b *testing.B, h int) (*transcript, func(time.Duration)) {
	b.Helper()
	ft := ldrender.NewFakeTerminal(100, h)
	tr := newTranscript(ft, 100, h, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	clock := time.Unix(0, 0)
	tr.now = func() time.Time { return clock }
	tr.enter()
	return tr, func(d time.Duration) { clock = clock.Add(d) }
}

// WHAT AN IDLE PAINT COSTS. The frame is identical and the resync is due, so
// this is the worst quiet case: every row compared, a buffer built and thrown
// away, nothing written.
func BenchmarkPaintUnchangedResyncDue(b *testing.B) {
	tr, advance := benchTranscript(b, 60)
	s := benchScreen(60)
	tr.paint(s)
	advance(time.Hour)
	b.ResetTimer()
	for b.Loop() {
		tr.paint(s)
	}
}

// The same frame with the resync NOT due: the ordinary quiet paint, which is
// what most of an idle session is.
func BenchmarkPaintUnchanged(b *testing.B) {
	tr, _ := benchTranscript(b, 60)
	s := benchScreen(60)
	tr.paint(s)
	b.ResetTimer()
	for b.Loop() {
		tr.paint(s)
	}
}

// And the frame a stream actually sends: one row different.
func BenchmarkPaintOneRowChanged(b *testing.B) {
	tr, _ := benchTranscript(b, 60)
	s := benchScreen(60)
	tr.paint(s)
	alt := append([]string(nil), s...)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		alt[30] = fmt.Sprintf("a changing row %d", i)
		tr.paint(append([]string(nil), alt...))
	}
}
