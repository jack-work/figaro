package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// A realistic "big aria" scroll rig.
//
// The pre-existing BenchmarkTranscript* set uses one-line prose messages, which
// hides the actual complaint: real arias are dominated by tool nodes carrying
// hundreds of output lines and multi-paragraph prose. Retained pages then hold
// tens of thousands of physical rows, and every frame re-materializes all of
// them even though only ~40 are visible.
//
// heavyMessages builds a message mix close to a working session:
//   - assistant prose, several wrapped paragraphs
//   - a thinking block
//   - two tool nodes, one with a large captured output
// ---------------------------------------------------------------------------

// Every turn in the rig carries the question that provoked it, because every
// turn in production does. Without one the renderer takes a different branch —
// no inquiry seam, and since the speaker header closes that seam, no header
// either — so the rig would measure a shape no reader ever sees, and its
// rows-per-message (which is the budget the retained window is spent in)
// would be wrong by two rows per turn.
func heavyInquiry(seed int) string {
	return fmt.Sprintf("Question %d: what does the transcript do with a heavy turn?", seed)
}

func heavyNodes(seed int, outputLines int) []livedoc.Node {
	var prose strings.Builder
	for p := range 3 {
		fmt.Fprintf(&prose, "Paragraph %d of message %d. ", p, seed)
		for range 6 {
			prose.WriteString("The quick brown fox jumps over the lazy dog and keeps running well past the right margin. ")
		}
		prose.WriteString("\n\n")
	}
	var out strings.Builder
	for i := range outputLines {
		fmt.Fprintf(&out, "%6d  internal/cli/transcript.go:%d: some captured tool output line with detail\n", i, i*7%997)
	}
	return []livedoc.Node{
		{Type: livedoc.NodeThinking, Markdown: "Considering the shape of the problem for message " + fmt.Sprint(seed) + ".\nA second line of thought."},
		{Type: livedoc.NodeProse, Markdown: prose.String()},
		{
			Type: livedoc.NodeTool, ID: fmt.Sprintf("t%d", seed), Name: "bash",
			Args:    map[string]any{"command": "rg --line-number transcript internal/cli | head -400"},
			Status:  livedoc.StatusOK,
			Summary: "rg --line-number transcript internal/cli",
			Output:  out.String(),
		},
		{Type: livedoc.NodeProse, Markdown: "Short follow-up prose for message " + fmt.Sprint(seed) + "."},
	}
}

// heavyTranscript builds a transcript whose retained window is full of large
// messages, entered and ready to scroll.
func heavyTranscript(b *testing.B, messages, outputLines int) (*transcript, *aria.Client) {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, messages)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Inquiry: heavyInquiry(i + 1), Nodes: heavyNodes(i+1, outputLines)}}
	}
	client.Apply(aria.Page{Parts: committed})
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr, client
}

// BenchmarkTranscriptScrollHeavy is the headline number: one j/k scroll step in
// a transcript whose retained pages are heavy. Nothing about the content
// changed — only the viewport offset — so an ideal implementation costs
// O(viewport), not O(retained rows).
func BenchmarkTranscriptScrollHeavy(b *testing.B) {
	for _, outputLines := range []int{20, 200} {
		b.Run(fmt.Sprintf("out%d", outputLines), func(b *testing.B) {
			tr, _ := heavyTranscript(b, 200, outputLines)
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

// BenchmarkTranscriptScrollBurst models a mouse-wheel flick: many scroll events
// arriving back to back. This is what feels laggy in practice.
func BenchmarkTranscriptScrollBurst(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 200)
	tr.scrollBy(-1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for range 12 {
			tr.scrollBy(-3)
		}
		for range 12 {
			tr.scrollBy(3)
		}
	}
}

// BenchmarkTranscriptRenderHeavy isolates a single frame with no state change.
func BenchmarkTranscriptRenderHeavy(b *testing.B) {
	for _, outputLines := range []int{20, 200} {
		b.Run(fmt.Sprintf("out%d", outputLines), func(b *testing.B) {
			tr, _ := heavyTranscript(b, 200, outputLines)
			tr.scrollBy(-1)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr.render()
			}
		})
	}
}

// BenchmarkTranscriptLinesHeavy isolates the row-materialization pass.
func BenchmarkTranscriptLinesHeavy(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 200)
	tr.scrollBy(-1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.lines()
	}
}

// BenchmarkTranscriptScrollHeavySearch scrolls with an active search
// highlight, which currently re-highlights every retained row per frame.
func BenchmarkTranscriptScrollHeavySearch(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 200)
	tr.matchQuery = "transcript"
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
}

// BenchmarkTranscriptHeavyMemory reports steady-state retained bytes rather
// than per-op cost: enter, scroll a while, and let -benchmem show the churn.
func BenchmarkTranscriptHeavyEnter(b *testing.B) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, 200)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Inquiry: heavyInquiry(i + 1), Nodes: heavyNodes(i+1, 200)}}
	}
	client.Apply(aria.Page{Parts: committed})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
		tr.enter()
	}
}
