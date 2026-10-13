package cli

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Per-frame cost while following the live tail in a heavy aria.

func heavyFollowTranscript(b *testing.B, messages, outputLines int) (*transcript, *aria.Client) {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, messages)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, outputLines)}}
	}
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr, client
}

func BenchmarkTranscriptFollowHeavy(b *testing.B) {
	tr, _ := heavyFollowTranscript(b, 200, 200)
	if !tr.follow {
		b.Fatal("expected follow mode")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.render()
	}
}

func BenchmarkTranscriptLiveHeavy(b *testing.B) {
	tr, client := heavyFollowTranscript(b, 200, 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: i, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
			"type":     string(livedoc.NodeProse),
			"markdown": fmt.Sprintf("live token stream, chunk %d, still going", i),
		}}}}}}}}, aria.Notify)
		tr.render()
	}
}

func BenchmarkTranscriptFindHeavy(b *testing.B) {
	tr, _ := heavyFollowTranscript(b, 200, 200)
	tr.scrollBy(-1)
	start := tr.offset
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.offset = start
		tr.find("Paragraph 1")
	}
}

func BenchmarkTranscriptFindRepeatHeavy(b *testing.B) {
	tr, _ := heavyFollowTranscript(b, 200, 200)
	tr.scrollBy(-1)
	tr.matchQuery = "Paragraph 1"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.findRepeat(1)
	}
}
