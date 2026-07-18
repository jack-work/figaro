package cli

import (
	"fmt"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func benchmarkTranscript(n int) *transcript {
	client := aria.NewClient()
	committed := make([]aria.Committed, n)
	for i := range committed {
		committed[i] = aria.Committed{
			LT:   i + 1,
			Role: "assistant",
			Nodes: []livedoc.Node{{
				Type:     livedoc.NodeProse,
				Markdown: "synthetic history",
			}},
		}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	tr := newTranscript(io.Discard, 120, 40, ldrender.NodeText{}, client, "benchmark", time.Now())
	_ = tr.lines()
	return tr
}

func BenchmarkTranscriptWarmRenderLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			tr := benchmarkTranscript(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				lines := tr.lines()
				runtime.KeepAlive(lines)
			}
		})
	}
}
