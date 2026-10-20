package cli

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// Both arms: the verbose-off twins prove the default view pays nothing.

func verboseTranscript(b *testing.B, messages, outputLines int, verbose bool) *transcript {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, messages)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{
			ID: uint64(i + 1), Inquiry: fmt.Sprintf("question %d", i+1),
			Sealed: true, Nodes: heavyNodes(i+1, outputLines),
		}}
	}
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
	tr := newTranscript(io.Discard, 100, 40,
		&ariaView{settings: &renderSettings{more: verbose}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr
}

func BenchmarkTranscriptVerboseEnter(b *testing.B) {
	for _, verbose := range []bool{false, true} {
		b.Run(fmt.Sprintf("verbose=%v", verbose), func(b *testing.B) {
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			committed := make([]aria.TurnPart, 200)
			for i := range committed {
				committed[i] = aria.TurnPart{Turn: aria.Turn{
					ID: uint64(i + 1), Inquiry: fmt.Sprintf("question %d", i+1),
					Sealed: true, Nodes: heavyNodes(i+1, 200),
				}}
			}
			client.Apply(aria.Page{Parts: committed}, aria.Notify)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr := newTranscript(io.Discard, 100, 40,
					&ariaView{settings: &renderSettings{more: verbose}}, client, "benchmark", time.Unix(0, 0))
				tr.enter()
			}
		})
	}
}

func BenchmarkTranscriptVerboseFrame(b *testing.B) {
	for _, verbose := range []bool{false, true} {
		b.Run(fmt.Sprintf("verbose=%v", verbose), func(b *testing.B) {
			tr := verboseTranscript(b, 200, 200, verbose)
			tr.scrollBy(-1)
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
	}
}

func BenchmarkTranscriptVerboseIndex(b *testing.B) {
	for _, verbose := range []bool{false, true} {
		b.Run(fmt.Sprintf("verbose=%v", verbose), func(b *testing.B) {
			tr := verboseTranscript(b, 200, 20, verbose)
			tr.stopFollowing()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr.buildIndex()
			}
		})
	}
}
