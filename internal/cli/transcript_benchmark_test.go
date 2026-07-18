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

func benchmarkTranscript(b *testing.B, messages int, nodes []livedoc.Node) (*transcript, *aria.Client) {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, messages)
	for i := range committed {
		messageNodes := nodes
		if messageNodes == nil {
			messageNodes = []livedoc.Node{{
				Type:     livedoc.NodeProse,
				Markdown: fmt.Sprintf("message %05d carries enough prose to wrap across a typical terminal row", i+1),
			}}
		}
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: messageNodes}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	return newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0)), client
}

func BenchmarkTranscriptStartup(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			tr, _ := benchmarkTranscript(b, messages, nil)
			b.ResetTimer()
			for range b.N {
				tr.rowCache = map[int]cachedMessage{}
				tr.prev = nil
				tr.enter()
			}
		})
	}
}

func BenchmarkTranscriptRender(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			tr, _ := benchmarkTranscript(b, messages, nil)
			tr.enter()
			b.ResetTimer()
			for range b.N {
				tr.render()
			}
		})
	}
}

func BenchmarkTranscriptSearchMiss(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			tr, _ := benchmarkTranscript(b, messages, nil)
			tr.enter()
			b.ResetTimer()
			for range b.N {
				tr.find("not present anywhere")
			}
		})
	}
}

func BenchmarkTranscriptPagedSearchMiss(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			history := transcriptHistory(messages)
			for range b.N {
				b.StopTimer()
				client := aria.NewClient()
				client.SetClosedLimit(transcriptTailLimit)
				client.Apply(readBefore(history, recentCursor, transcriptPageSize))
				tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
				tr.enter()
				b.StartTimer()
				tr.find("not present anywhere")
				for tr.searchingHistory() {
					req, ok := tr.pageCursor()
					if !ok {
						break
					}
					tr.applyPage(req, readBefore(history, req.before, transcriptPageSize))
				}
			}
		})
	}
}

func BenchmarkTranscriptSelection(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			tr, _ := benchmarkTranscript(b, messages, nil)
			tr.enter()
			b.ResetTimer()
			for range b.N {
				tr.selectNode(-1, false)
			}
		})
	}
}

func BenchmarkTranscriptResize(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			tr, _ := benchmarkTranscript(b, messages, nil)
			tr.enter()
			b.ResetTimer()
			for i := range b.N {
				tr.resize(99+i%2, 40)
			}
		})
	}
}

func BenchmarkTranscriptLiveUpdate(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			tr, client := benchmarkTranscript(b, messages, nil)
			tr.enter()
			b.ResetTimer()
			for i := range b.N {
				client.Apply(aria.AriaRead{Live: &aria.Live{
					LT: messages + 1,
					V:  i,
					Nodes: []aria.NodeDelta{{
						ID: "live",
						Set: map[string]any{
							"type":     string(livedoc.NodeProse),
							"markdown": fmt.Sprintf("live update %d", i),
						},
					}},
				}})
				tr.render()
			}
		})
	}
}

func BenchmarkTranscriptLargeToolOutput(b *testing.B) {
	for _, size := range []int{100 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			output := strings.Repeat("0123456789abcdef0123456789abcdef\n", size/33)
			nodes := []livedoc.Node{{
				Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: output,
			}}
			tr, _ := benchmarkTranscript(b, 1, nodes)
			b.ResetTimer()
			for range b.N {
				tr.rowCache = map[int]cachedMessage{}
				tr.prev = nil
				tr.enter()
			}
		})
	}
}
