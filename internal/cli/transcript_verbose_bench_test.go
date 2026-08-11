package cli

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The VERBOSE render path had no benchmark. Ctrl-O now adds a row per node
// (transcript_coords.go), and a path that cannot be measured cannot be
// defended: so here it is, in both arms of the A/B: cold entry (which fills
// the row cache, where the coordinate row is minted) and a steady frame (which
// only reads it back).
//
// The verbose-OFF twins are here on purpose too. They are the DEFAULT view,
// and the claim being made is that the default pays nothing at all; a
// benchmark that only measures the arm that changed cannot support it.

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
	client.Apply(aria.Page{Parts: committed})
	tr := newTranscript(io.Discard, 100, 40,
		&ariaView{settings: &renderSettings{verbose: verbose}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr
}

// BenchmarkTranscriptVerboseEnter is the cold path: every message rendered
// into the row cache, which is where a coordinate row costs anything at all.
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
			client.Apply(aria.Page{Parts: committed})
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr := newTranscript(io.Discard, 100, 40,
					&ariaView{settings: &renderSettings{verbose: verbose}}, client, "benchmark", time.Unix(0, 0))
				tr.enter()
			}
		})
	}
}

// BenchmarkTranscriptVerboseFrame is the per-frame path with the cache warm:
// composition only. The coordinate rows are already materialized, so this is
// where a "one more row per node" claim is actually cashed out.
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

// BenchmarkTranscriptVerboseIndex isolates buildIndex: the O(#messages) pass
// every frame makes: because that is the one whose input size the extra rows
// change (taller entries, same entry count).
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
