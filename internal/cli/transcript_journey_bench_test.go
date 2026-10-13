package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Paging policy: follow-mode frames and a long journey through history,
// counting fetches, evictions and node re-renders.

// pagingView counts Render calls.
type pagingView struct {
	inner  *ariaView
	render int
}

func (v *pagingView) Render(n livedoc.Node, width, tick int) []string {
	return v.RenderExpanded(n, width, tick, false)
}

func (v *pagingView) RenderExpanded(n livedoc.Node, width, tick int, full bool) []string {
	v.render++
	return v.inner.RenderExpanded(n, width, tick, full)
}

// pagingHarness drives key, then pageCursor -> read -> applyPage, as the
// input loop does.
type pagingHarness struct {
	tr      *transcript
	client  *aria.Client
	view    *pagingView
	history []aria.TurnPart

	fetches     int
	fetchedMsgs int
	keys        int
	latency     time.Duration
	blocked     time.Duration

	held      map[int]bool
	evictions int
	refetches int
	seen      map[int]bool

	// peakBytes is sampled after every applyPage: retained row memory is what a
	// bigger window costs once the frame is O(viewport).
	peakBytes int
	peakRows  int
}

func newPagingHarness(messages, outputLines, w, h int) *pagingHarness {
	history := make([]aria.TurnPart, messages)
	for i := range history {
		history[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, outputLines)}}
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	view := &pagingView{inner: &ariaView{settings: &renderSettings{}}}
	tr := newTranscript(io.Discard, w, h, view, client, "journey", time.Unix(0, 0))
	h0 := &pagingHarness{
		tr: tr, client: client, view: view, history: history,
		held: map[int]bool{}, seen: map[int]bool{},
	}
	tr.enter()
	h0.sync()
	return h0
}

func (h *pagingHarness) sync() {
	for range 8 {
		req, need := h.tr.pageCursor()
		if !need {
			return
		}
		h.tr.applyPage(req, h.read(req))
		h.account()
	}
}

func (h *pagingHarness) read(req transcriptPageRequest) aria.Page {
	start := time.Now()
	if h.latency > 0 {
		time.Sleep(h.latency)
	}
	h.blocked += time.Since(start)
	h.fetches++
	limit := req.limit
	if limit <= 0 {
		limit = transcriptPageSize
	}
	at := req.at
	page := readBeforeAt(h.history, at, limit)
	msgs := pageMessages(page)
	h.fetchedMsgs += len(msgs)
	for _, m := range msgs {
		if h.seen[m.Turn] {
			h.refetches++
		}
		h.seen[m.Turn] = true
	}
	return page
}

func (h *pagingHarness) account() {
	now := map[int]bool{}
	for _, m := range h.tr.messages() {
		now[m.Turn] = true
	}
	for lt := range h.held {
		if !now[lt] {
			h.evictions++
		}
	}
	h.held = now
	rows, structBytes, textBytes, _ := h.tr.rowCacheFootprint()
	if b := structBytes + textBytes; b > h.peakBytes {
		h.peakBytes = b
	}
	if rows > h.peakRows {
		h.peakRows = rows
	}
}

func (h *pagingHarness) key(b byte) {
	h.keys++
	h.tr.key(b)
	h.sync()
}

// journey is measured in messages, not pages, so the trip is the same at
// every page geometry.
func (h *pagingHarness) journey(back int) {
	target := int(h.tr.from.Turn) - back
	for range 20000 {
		if int(h.tr.from.Turn) <= target {
			break
		}
		if h.tr.atAriaFloor() && h.tr.offset == 0 {
			break
		}
		h.key('u')
	}
	for range 20000 {
		if h.tr.offset >= len(h.tr.lineKey)-h.tr.h {
			break
		}
		h.key('d')
	}
}

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
				h.journey(120)
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

// A follow frame: nothing about the retained window changed.
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

func BenchmarkTranscriptLiveStream(b *testing.B) {
	tr, client := heavyTranscript(b, 200, 200)
	tr.follow = true
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": "streaming"}}}}}}}}, aria.Notify)
	tr.render()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%4 == 0 {
			client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(201), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
				"type": "prose", "markdown": fmt.Sprintf("streaming token %d", i)}}}}}}}}, aria.Notify)
		}
		tr.tick++
		tr.render()
	}
}

// A report, not an assertion: FIGARO_PAGING_REPORT=1 go test -run JourneyCost -v.
func TestTranscriptJourneyCost(t *testing.T) {
	if os.Getenv("FIGARO_PAGING_REPORT") == "" {
		t.Skip("set FIGARO_PAGING_REPORT=1 for the paging cost report")
	}
	h := newPagingHarness(600, 20, 100, 40)
	start := time.Now()
	h.journey(300)
	t.Logf("geometry pageSize=%d pageLimit=%d rows/window=%d",
		transcriptPageSize, transcriptPageLimit, len(h.tr.lineKey))
	t.Logf("journey: keys=%d fetches=%d fetchedMsgs=%d refetchedMsgs=%d evictions=%d nodeRenders=%d wall=%s",
		h.keys, h.fetches, h.fetchedMsgs, h.refetches, h.evictions, h.view.render, time.Since(start).Round(time.Millisecond))
}

var _ = sort.Search
