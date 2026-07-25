package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// Axis D rig: paging POLICY, not per-frame rendering.
//
// The shared scroll rig (transcript_scroll_bench_test.go) measures the cost of
// one frame with the window held still. This file measures the things the
// window itself does:
//
//   - follow-mode frames, which currently rebuild the whole page window per
//     frame via resetToTail (BenchmarkTranscriptFollowFrame / LiveStream)
//   - a long journey: scroll from the tail back over several page boundaries
//     and return, counting fetches, evictions, and node re-renders
//     (BenchmarkTranscriptJourney + TestTranscriptJourneyCost, which prints
//     the policy counters)
// ---------------------------------------------------------------------------

// countingView wraps a NodeView and counts Render calls, so a test can measure
// how much prose the paging policy forces us to re-render. Counting from the
// view keeps the instrumentation entirely in test code.
type countingView struct {
	inner  *ariaView
	render int
}

func (v *countingView) Render(n livedoc.Node, width, tick int) []string {
	return v.RenderExpanded(n, width, tick, false)
}

func (v *countingView) RenderExpanded(n livedoc.Node, width, tick int, full bool) []string {
	v.render++
	return v.inner.RenderExpanded(n, width, tick, full)
}

// pagingHarness drives a transcript exactly the way the input loop does:
// key/scroll, then pageCursor -> read -> applyPage, and serves ReadBefore out
// of an in-memory history (optionally with injected latency).
type pagingHarness struct {
	tr      *transcript
	client  *aria.Client
	view    *countingView
	history []aria.Committed

	fetches     int
	fetchedMsgs int
	keys        int
	latency     time.Duration
	blocked     time.Duration // wall time spent inside synchronous fetches

	// eviction accounting, derived from the retained page set
	held      map[int]bool // LT -> retained right now
	evictions int
	refetches int
	seen      map[int]bool // LT -> has been fetched at least once
}

func newPagingHarness(messages, outputLines, w, h int) *pagingHarness {
	history := make([]aria.Committed, messages)
	for i := range history {
		history[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, outputLines)}
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	view := &countingView{inner: &ariaView{settings: &renderSettings{}}}
	tr := newTranscript(io.Discard, w, h, view, client, "journey", time.Unix(0, 0))
	h0 := &pagingHarness{
		tr: tr, client: client, view: view, history: history,
		held: map[int]bool{}, seen: map[int]bool{},
	}
	tr.enter()
	h0.sync()
	return h0
}

// sync mirrors the input loop's post-key `pageTranscript()` call, including the
// synchronous ReadBefore that currently sits on the critical path.
func (h *pagingHarness) sync() {
	for range 8 { // a single key can chain at most a couple of pages
		req, need := h.tr.pageCursor()
		if !need {
			return
		}
		messages := h.read(req)
		h.tr.applyPage(req, messages)
		h.account()
	}
}

func (h *pagingHarness) read(req transcriptPageRequest) []aria.Message {
	if len(req.cached) > 0 {
		return req.cached // served from the payload LRU: no I/O
	}
	start := time.Now()
	if h.latency > 0 {
		time.Sleep(h.latency)
	}
	h.blocked += time.Since(start)
	h.fetches++
	read := func(before, limit int) (aria.AriaRead, error) {
		return readBefore(h.history, before, limit), nil
	}
	var r aria.AriaRead
	if req.after != 0 {
		r, _ = readNextPage(req.after, req.watermark, transcriptPageSize, read)
	} else {
		limit := transcriptPageSize
		if req.expected.Count != 0 {
			limit = req.expected.Count
		}
		r, _ = read(req.before, limit)
	}
	msgs := committedMessages(r.Committed)
	h.fetchedMsgs += len(msgs)
	for _, m := range msgs {
		if h.seen[m.LT] {
			h.refetches++
		}
		h.seen[m.LT] = true
	}
	return msgs
}

// account diffs the retained window against the previous one to count evictions.
func (h *pagingHarness) account() {
	now := map[int]bool{}
	for _, m := range h.tr.messages() {
		now[m.LT] = true
	}
	for lt := range h.held {
		if !now[lt] {
			h.evictions++
		}
	}
	h.held = now
}

func (h *pagingHarness) key(b byte) {
	h.keys++
	h.tr.key(b)
	h.sync()
}

func (h *pagingHarness) scroll(delta int) {
	h.tr.scrollBy(delta)
	h.sync()
}

// journey scrolls back (half-page at a time, like 'u') until `pages` older
// pages have been pulled in, then scrolls all the way back down to the tail.
// This is "go find that thing 500 messages ago, then come back".
func (h *pagingHarness) journey(pages int) {
	target := h.fetches + pages
	for range 20000 {
		if h.fetches >= target || h.tr.noMoreOlder && h.tr.offset == 0 {
			break
		}
		h.key('u')
	}
	for range 20000 {
		if h.tr.offset >= len(h.tr.lineLT)-h.tr.h && !h.tr.hasNewerHistory() {
			break
		}
		h.key('d')
	}
}

// BenchmarkTranscriptJourney is the artifact for axis D: scroll well back into
// history and come back. It exercises fetch, eviction, refetch and re-render
// churn rather than steady-state frame cost, and reports the policy counters
// (fetches, evicted messages, refetched messages, node re-renders) per trip.
func BenchmarkTranscriptJourney(b *testing.B) {
	for _, out := range []int{20, 200} {
		b.Run(fmt.Sprintf("out%d", out), func(b *testing.B) {
			var fetches, refetches, evictions, keys, renders int
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				h := newPagingHarness(600, out, 100, 40)
				b.StartTimer()
				h.journey(4)
				b.StopTimer()
				fetches += h.fetches
				refetches += h.refetches
				evictions += h.evictions
				keys += h.keys
				renders += h.view.render
				b.StartTimer()
			}
			b.StopTimer()
			n := float64(max(b.N, 1))
			b.ReportMetric(float64(fetches)/n, "fetches/op")
			b.ReportMetric(float64(refetches)/n, "refetched-msgs/op")
			b.ReportMetric(float64(evictions)/n, "evicted-msgs/op")
			b.ReportMetric(float64(renders)/n, "noderenders/op")
			b.ReportMetric(float64(keys)/n, "keys/op")
		})
	}
}

// BenchmarkTranscriptFollowFrame is a live-streaming frame: the pager is at the
// bottom following the tail, and something (a token, a spinner tick) forces a
// repaint. Nothing about the retained window changed.
func BenchmarkTranscriptFollowFrame(b *testing.B) {
	tr, _ := heavyTranscript(b, 200, 200)
	tr.follow = true
	tr.render()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.render()
	}
}

// BenchmarkTranscriptLiveStream is the honest version: a spinner tick arrives,
// then a token appends to the open message, over and over. This is what the
// pager does for the whole duration of a turn.
func BenchmarkTranscriptLiveStream(b *testing.B) {
	tr, client := heavyTranscript(b, 200, 200)
	tr.follow = true
	client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: 201, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{ID: "n0", Set: map[string]any{"type": "prose", "markdown": "streaming"}}},
	}})
	tr.render()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%4 == 0 {
			client.Apply(aria.AriaRead{Live: &aria.Live{
				LT: 201, V: 0, Role: "assistant",
				Nodes: []aria.NodeDelta{{ID: "n0", Set: map[string]any{
					"type": "prose", "markdown": fmt.Sprintf("streaming token %d", i)}}},
			}})
		}
		tr.tick++
		tr.render()
	}
}

// TestTranscriptJourneyCost is not an assertion, it is a report: it prints the
// policy counters for a round trip through history so the geometry choice has
// numbers behind it. Off by default (it is a slow, whole-journey run); enable
// with FIGARO_PAGING_REPORT=1 go test -run JourneyCost -v.
func TestTranscriptJourneyCost(t *testing.T) {
	if os.Getenv("FIGARO_PAGING_REPORT") == "" {
		t.Skip("set FIGARO_PAGING_REPORT=1 for the paging cost report")
	}
	h := newPagingHarness(600, 20, 100, 40)
	start := time.Now()
	h.journey(10)
	t.Logf("geometry pageSize=%d pageLimit=%d rows/window=%d",
		transcriptPageSize, transcriptPageLimit, len(h.tr.lineLT))
	t.Logf("journey: keys=%d fetches=%d fetchedMsgs=%d refetchedMsgs=%d evictions=%d nodeRenders=%d wall=%s",
		h.keys, h.fetches, h.fetchedMsgs, h.refetches, h.evictions, h.view.render, time.Since(start).Round(time.Millisecond))
}

// sanity: readBefore in the shared test helper is ascending and exclusive.
var _ = sort.Search
