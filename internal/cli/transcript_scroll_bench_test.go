package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// A big-aria rig: tool nodes with hundreds of output lines and multi-paragraph
// prose, the shape one-line fixtures hide.

// Without a question the renderer draws no inquiry seam and no speaker
// header, which is two rows per turn off the window budget.
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

func heavyTranscript(b *testing.B, messages, outputLines int) (*transcript, *aria.Client) {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, messages)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Inquiry: heavyInquiry(i + 1), Nodes: heavyNodes(i+1, outputLines)}}
	}
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr, client
}

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

func BenchmarkTranscriptLinesHeavy(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 200)
	tr.scrollBy(-1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.lines()
	}
}

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

func BenchmarkTranscriptHeavyEnter(b *testing.B) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, 200)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Inquiry: heavyInquiry(i + 1), Nodes: heavyNodes(i+1, 200)}}
	}
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
		tr.enter()
	}
}
