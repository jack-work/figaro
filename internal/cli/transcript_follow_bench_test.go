package cli

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The shared scroll rig covers the "held" viewport (follow == false). This file
// covers the other per-frame path: following the live tail in a heavy aria,
// where every frame re-derives the retained window from the client view before
// it can draw anything.

func heavyFollowTranscript(b *testing.B, messages, outputLines int) (*transcript, *aria.Client) {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, messages)
	for i := range committed {
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, outputLines)}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr, client
}

// BenchmarkTranscriptFollowHeavy is a frame drawn while pinned to the live
// tail with nothing at all changing: the pure cost of re-deriving the retained
// window every frame.
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

// BenchmarkTranscriptLiveHeavy is a streaming frame in a heavy aria: one live
// delta applied to the open message, then a repaint.
func BenchmarkTranscriptLiveHeavy(b *testing.B) {
	tr, client := heavyFollowTranscript(b, 200, 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		client.Apply(aria.AriaRead{Live: &aria.Live{
			LT: 201, V: i,
			Nodes: []aria.NodeDelta{{ID: "live", Set: map[string]any{
				"type":     string(livedoc.NodeProse),
				"markdown": fmt.Sprintf("live token stream, chunk %d, still going", i),
			}}},
		}})
		tr.render()
	}
}

// BenchmarkTranscriptFindHeavy is a '/' jump to a match a few lines below the
// cursor in a heavy aria. The match is near, so the cost should be the cost of
// the lines actually inspected — not of the whole retained window.
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

// BenchmarkTranscriptFindRepeatHeavy is n/N through the same window.
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
