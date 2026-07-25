package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
)

func benchmarkTranscript(b *testing.B, messages int, nodes []livedoc.Node) (*transcript, *aria.Client) {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, messages)
	for i := range committed {
		messageNodes := nodes
		if messageNodes == nil {
			messageNodes = []livedoc.Node{{
				Type:     livedoc.NodeProse,
				Markdown: fmt.Sprintf("message %05d carries enough prose to wrap across a typical terminal row", i+1),
			}}
		}
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: messageNodes}}
	}
	client.Apply(aria.Page{Parts: committed})
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
					tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
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
				client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(messages + 1), Live: &aria.Live{From: 0, V: i, Nodes: []aria.NodeDelta{{
					ID: 0,
					Set: map[string]any{
						"type":     string(livedoc.NodeProse),
						"markdown": fmt.Sprintf("live update %d", i),
					},
				}}}}}}})
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

func BenchmarkTranscriptSelectionRehydrate(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			history := transcriptHistory(messages)
			plan := selectionCopyPlan{
				lo: testSelectionPoint(1, 0, history[0].Nodes[0]),
				hi: testSelectionPoint(messages, 0, history[messages-1].Nodes[0]),
			}
			b.ResetTimer()
			for range b.N {
				_, err := selectionText(plan, transcriptPageSize, func(before, limit int) (aria.Page, error) {
					return readBefore(history, before, limit), nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTranscriptDescriptorFallback(b *testing.B) {
	for _, messages := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("%d", messages), func(b *testing.B) {
			history := transcriptHistory(messages)
			after := messages / 2
			probes := 0
			b.ResetTimer()
			for range b.N {
				_, err := readNextPage(after, messages, transcriptPageSize, func(before, limit int) (aria.Page, error) {
					probes++
					return readBefore(history, before, limit), nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(probes)/float64(b.N), "probes/op")
		})
	}
}

func BenchmarkTranscriptSelectedLargeToolRender(b *testing.B) {
	output := strings.Repeat("0123456789abcdef0123456789abcdef\n", (1<<20)/33)
	nodes := []livedoc.Node{{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: output,
	}}
	tr, _ := benchmarkTranscript(b, 1, nodes)
	tr.enter()
	tr.selectNode(-1, false)
	b.ResetTimer()
	for range b.N {
		tr.render()
	}
}

func BenchmarkTranscriptHistoricalSearchCancel(b *testing.B) {
	reader := &benchmarkSearchReader{
		started:  make(chan struct{}, 1),
		canceled: make(chan struct{}, 1),
	}
	in := newSearchInteractiveInput(reader, newSearchInputTerminal())
	for b.Loop() {
		b.StopTimer()
		in.mu.Lock()
		in.lt.tr.find("absent")
		in.mu.Unlock()
		in.pageTranscript()
		<-reader.started
		in.mu.Lock()
		done := in.searchDone
		in.mu.Unlock()
		b.StartTimer()

		in.cancelTranscriptSearch()
		<-done
		<-reader.canceled
	}
}

type benchmarkSearchReader struct {
	started  chan struct{}
	canceled chan struct{}
}

func (r *benchmarkSearchReader) Read(context.Context, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *benchmarkSearchReader) ReadBefore(ctx context.Context, _, _ int) (aria.Page, error) {
	r.started <- struct{}{}
	<-ctx.Done()
	r.canceled <- struct{}{}
	return aria.Page{}, ctx.Err()
}

func (r *benchmarkSearchReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}
