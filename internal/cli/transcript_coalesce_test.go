package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// Input coalescing and the frame-rate ceiling.
//
// A mouse-wheel flick delivers a burst of scroll reports. The pager used to
// paint one frame per report, so a 24-event flick meant 24 full frames, 23 of
// which nobody wanted to see, and each of which delayed the one they did.
// These tests pin both halves of the fix: a burst produces exactly one frame
// at the settled offset, and no rate limiter ever swallows the final frame.
// ---------------------------------------------------------------------------

func coalesceTranscript(tb testing.TB, out *countingWriter) *transcript {
	tb.Helper()
	return heavyTranscriptOn(tb, out, 40, 20)
}

// TestTranscriptBatch_ManyScrollsOneFrame is the headline guarantee: N queued
// scroll events paint once and land exactly where N sequential scrolls would.
func TestTranscriptBatch_ManyScrollsOneFrame(t *testing.T) {
	var sequential, batched countingWriter
	seq := coalesceTranscript(t, &sequential)
	bat := coalesceTranscript(t, &batched)

	deltas := []int{-3, -3, -3, -3, -3, -3, -3, -3, 3, -3, -3, -3, -3, -3, -3, -3, -3, -3, -3, -3, -3, -3, -3, -3}

	sequential.reset()
	for _, d := range deltas {
		seq.scrollBy(d)
	}
	if got := sequential.writes.Load(); got != int64(len(deltas)) {
		t.Fatalf("unbatched scrolls painted %d frames, want %d (the baseline behaviour)", got, len(deltas))
	}

	batched.reset()
	bat.beginBatch()
	for _, d := range deltas {
		bat.scrollBy(d)
	}
	bat.endBatch()
	if got := batched.writes.Load(); got != 1 {
		t.Fatalf("batched burst of %d scrolls painted %d frames, want 1", len(deltas), got)
	}
	if seq.offset != bat.offset {
		t.Fatalf("batched burst landed at offset %d, sequential at %d", bat.offset, seq.offset)
	}
	if strings.Join(seq.prev, "\n") != strings.Join(bat.prev, "\n") {
		t.Fatal("batched burst painted a different screen than the sequential scrolls")
	}
	if bat.dirty {
		t.Fatal("endBatch left the screen dirty")
	}
}

// TestTranscriptBatch_NestingAndEmptyBatch checks the bookkeeping: nested
// brackets paint once at the outermost close, and a batch with no state change
// paints nothing at all.
func TestTranscriptBatch_NestingAndEmptyBatch(t *testing.T) {
	var w countingWriter
	tr := coalesceTranscript(t, &w)

	w.reset()
	tr.beginBatch()
	tr.beginBatch()
	tr.scrollBy(-2)
	tr.endBatch()
	if got := w.writes.Load(); got != 0 {
		t.Fatalf("inner endBatch painted %d frames, want 0 (still batched)", got)
	}
	tr.endBatch()
	if got := w.writes.Load(); got != 1 {
		t.Fatalf("outer endBatch painted %d frames, want 1", got)
	}

	w.reset()
	tr.beginBatch()
	tr.endBatch()
	if got := w.writes.Load(); got != 0 {
		t.Fatalf("empty batch painted %d frames, want 0", got)
	}
}

// TestTranscriptGate_TrailingFlushIsOwed proves the invariant a rate limiter
// lives or dies by: a refused frame is remembered, and flush draws it. It also
// checks that flush is a no-op when nothing is pending, and after leave().
func TestTranscriptGate_TrailingFlushIsOwed(t *testing.T) {
	var w countingWriter
	tr := coalesceTranscript(t, &w)
	allow := true
	painted := 0
	tr.gate = func() bool { return allow }
	tr.painted = func() { painted++ }

	allow = false
	w.reset()
	for range 10 {
		tr.scrollBy(-1)
	}
	if got := w.writes.Load(); got != 0 {
		t.Fatalf("gated scrolls painted %d frames, want 0", got)
	}
	if !tr.dirty {
		t.Fatal("a refused frame must leave the screen marked dirty")
	}
	tr.flush()
	if got := w.writes.Load(); got != 1 {
		t.Fatalf("flush painted %d frames, want 1", got)
	}
	if painted != 1 {
		t.Fatalf("painted hook fired %d times, want 1", painted)
	}
	tr.flush()
	if got := w.writes.Load(); got != 1 {
		t.Fatal("flush with nothing pending must not paint")
	}

	allow = false
	tr.scrollBy(-1)
	tr.leave()
	w.reset()
	tr.flush()
	if got := w.writes.Load(); got != 0 {
		t.Fatalf("flush after leave painted %d frames, want 0", got)
	}
}

// TestFramePacer_CeilingAndTrailingRender drives the pacer on a fake clock:
// one frame per interval, and the burst's final state always painted.
func TestFramePacer_CeilingAndTrailingRender(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	var timers []func()
	var flushes int
	p := &framePacer{
		min:   10 * time.Millisecond,
		lock:  &mu,
		now:   func() time.Time { return now },
		after: func(d time.Duration, fn func()) { timers = append(timers, fn) },
		flush: func() { flushes++ },
	}

	if !p.allow() {
		t.Fatal("the first frame must never be delayed")
	}
	p.painted()
	for i := range 20 { // a burst inside one frame interval
		now = now.Add(200 * time.Microsecond)
		if p.allow() {
			t.Fatalf("event %d was allowed to paint inside the frame interval", i)
		}
	}
	if len(timers) != 1 {
		t.Fatalf("a burst armed %d trailing renders, want exactly 1", len(timers))
	}
	now = now.Add(10 * time.Millisecond)
	timers[0]()
	if flushes != 1 {
		t.Fatalf("trailing render flushed %d times, want 1", flushes)
	}
	// The budget has elapsed; the next event paints immediately again.
	p.painted()
	now = now.Add(50 * time.Millisecond)
	if !p.allow() {
		t.Fatal("an event a full interval later must paint immediately")
	}
}

// TestFramePacer_ZeroIntervalIsTransparent: without setRenderLock the pager
// must behave exactly as before.
func TestFramePacer_ZeroIntervalIsTransparent(t *testing.T) {
	p := &framePacer{now: time.Now}
	for range 5 {
		if !p.allow() {
			t.Fatal("a pacer with no interval must allow every frame")
		}
	}
}

// ---------------------------------------------------------------------------
// The input loop.
// ---------------------------------------------------------------------------

type stubReadClient struct{}

func (stubReadClient) Read(context.Context, int) (aria.Page, error) {
	return aria.Page{}, nil
}
func (stubReadClient) ReadBefore(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}
func (stubReadClient) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func coalesceInput(tb testing.TB, out *countingWriter) (*interactiveInput, *livelogTurn) {
	tb.Helper()
	committed := make([]aria.TurnPart, 40)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 20)}}
	}

	settings := &renderSettings{}
	status := newSessionStatus("aria0001", time.Unix(0, 0))
	lt := newLivelogTurn(out, 100, 40, settings, "aria0001", time.Unix(0, 0), status, nil, nil)
	// Enter the pager BEFORE feeding history, as the real flow does: the aria
	// callbacks route to the transcript only while it is up.
	lt.enterTranscript()
	lt.apply(aria.Page{Parts: committed})
	return &interactiveInput{
		tc: nil, lt: lt, fcli: stubReadClient{}, mu: &sync.Mutex{}, set: settings,
		figaroID: "aria0001", cancel: func() {},
		disconnectCh: make(chan struct{}, 1),
	}, lt
}

func wheelReports(n int, up bool) []byte {
	code := 65
	if up {
		code = 64
	}
	var b strings.Builder
	for i := range n {
		b.WriteString("\x1b[<")
		b.WriteString(itoaTest(code))
		b.WriteString(";")
		b.WriteString(itoaTest(10 + i%3))
		b.WriteString(";20M")
	}
	return []byte(b.String())
}

func itoaTest(n int) string { return string(appendUint(nil, n)) }

// TestInputConsume_WheelBurstPaintsOneFrame is the end-to-end coalescing test:
// a flick's worth of wheel reports delivered in one read must produce one frame
// and land at the same offset as the same reports delivered one at a time.
func TestInputConsume_WheelBurstPaintsOneFrame(t *testing.T) {
	const events = 24
	burst := wheelReports(events, true)

	var oneAtATime countingWriter
	drip, _ := coalesceInput(t, &oneAtATime)
	oneAtATime.reset()
	for _, ev := range splitReports(burst) {
		if rest, stop := drip.consume(ev); stop || len(rest) != 0 {
			t.Fatalf("consume(%q) = %q, %v", ev, rest, stop)
		}
	}
	if got := oneAtATime.writes.Load(); got != events {
		t.Fatalf("one read per event painted %d frames, want %d", got, events)
	}
	drippedBytes := oneAtATime.bytes.Load()

	var coalesced countingWriter
	in, lt := coalesceInput(t, &coalesced)
	coalesced.reset()
	rest, stop := in.consume(burst)
	if stop || len(rest) != 0 {
		t.Fatalf("consume(burst) = %q, %v", rest, stop)
	}
	if got := coalesced.writes.Load(); got != 1 {
		t.Fatalf("a %d-event flick in one read painted %d frames, want 1", events, got)
	}
	if lt.tr.offset != drip.lt.tr.offset {
		t.Fatalf("coalesced flick landed at offset %d, dripped at %d", lt.tr.offset, drip.lt.tr.offset)
	}
	// The flick's total travel (72 rows) exceeds the viewport, so the single
	// frame is a genuine full repaint, and still costs well under half of what
	// the 24 intermediate frames did.
	if b := coalesced.bytes.Load(); b*2 >= drippedBytes {
		t.Fatalf("coalesced flick emitted %d bytes vs %d dripped; want less than half", b, drippedBytes)
	}
}

// TestInputConsume_KeyBurstPaintsOneFrame: a held-down j (autorepeat) arrives
// as a run of bytes in one read and must also collapse to one frame.
func TestInputConsume_KeyBurstPaintsOneFrame(t *testing.T) {
	var w countingWriter
	in, lt := coalesceInput(t, &w)
	lt.tr.stopFollowing() // measure pure motion: leaving live also reclaims the padding row
	before := lt.tr.offset
	w.reset()
	if rest, stop := in.consume([]byte(strings.Repeat("k", 12))); stop || len(rest) != 0 {
		t.Fatalf("consume = %q, %v", rest, stop)
	}
	if got := w.writes.Load(); got != 1 {
		t.Fatalf("12 autorepeated k's painted %d frames, want 1", got)
	}
	if lt.tr.offset != before-12 {
		t.Fatalf("offset moved to %d, want %d", lt.tr.offset, before-12)
	}
}

// TestInputConsume_SplitEscapeStillCoalesces: an escape sequence chopped across
// two reads must be stitched, not dropped, and must not cost extra frames.
func TestInputConsume_SplitEscapeStillCoalesces(t *testing.T) {
	var w countingWriter
	in, lt := coalesceInput(t, &w)
	burst := wheelReports(6, true)
	cut := len(burst) - 3
	lt.tr.stopFollowing() // as above: the first motion out of live reclaims the padding row
	before := lt.tr.offset

	w.reset()
	rest, stop := in.consume(burst[:cut])
	if stop {
		t.Fatal("consume must not stop")
	}
	if len(rest) == 0 {
		t.Fatal("a truncated wheel report must be held for the next read")
	}
	if got := w.writes.Load(); got != 1 {
		t.Fatalf("first chunk painted %d frames, want 1", got)
	}
	if _, stop := in.consume(append(rest, burst[cut:]...)); stop {
		t.Fatal("consume must not stop")
	}
	if got := w.writes.Load(); got != 2 {
		t.Fatalf("two reads painted %d frames, want 2", got)
	}
	if lt.tr.offset != before-18 { // 6 reports x 3 lines up
		t.Fatalf("offset moved to %d, want %d", lt.tr.offset, before-18)
	}
}

// splitReports chops a burst into individual SGR mouse reports.
func splitReports(burst []byte) [][]byte {
	var out [][]byte
	for i := 0; i < len(burst); {
		j := i + 1
		for j < len(burst) && burst[j] != 0x1b {
			j++
		}
		out = append(out, burst[i:j])
		i = j
	}
	return out
}

// BenchmarkTranscriptBurstFrames is the coalescing counterpart to the shared
// rig's BenchmarkTranscriptScrollBurst: a 24-event flick drained as one batch.
// The flick nets out somewhere new (18 up, 6 down) so the single frame has real
// work to do. frames/op is the metric the shared rig cannot show.
func BenchmarkTranscriptBurstFrames(b *testing.B) {
	var w countingWriter
	tr := heavyTranscriptOn(b, &w, 200, 200)
	tr.scrollBy(-2000)
	w.reset()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		tr.beginBatch()
		dir := -3
		if i%2 == 1 {
			dir = 3
		}
		for range 18 {
			tr.scrollBy(dir)
		}
		for range 6 {
			tr.scrollBy(-dir)
		}
		tr.endBatch()
	}
	b.StopTimer()
	b.ReportMetric(float64(w.writes.Load())/float64(b.N), "frames/op")
	b.ReportMetric(float64(w.bytes.Load())/float64(b.N), "B/op")
}

// TestLivelogTurn_StreamDeltasArePaced is the wiring test: live aria frames go
// through the same dirty-flag path as input, so a burst of stream deltas paints
// once per frame interval and the settled state is always painted by the
// trailing render.
func TestLivelogTurn_StreamDeltasArePaced(t *testing.T) {
	var w countingWriter
	var mu sync.Mutex
	settings := &renderSettings{}
	status := newSessionStatus("aria0001", time.Unix(0, 0))
	lt := newLivelogTurn(&w, 100, 30, settings, "aria0001", time.Unix(0, 0), status, nil, nil)

	now := time.Unix(2000, 0)
	var timers []func()
	lt.pace.now = func() time.Time { return now }
	lt.pace.after = func(_ time.Duration, fn func()) { timers = append(timers, fn) }
	lt.setRenderLock(&mu)
	lt.enterTranscript()
	lt.apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: uint64(1), Sealed: true, Nodes: heavyNodes(1, 10)}},
	}})

	// Settle: entering the pager already armed a trailing render, and the pacer
	// keeps only one in flight at a time.
	now = now.Add(transcriptFrameInterval)
	for _, fn := range timers {
		fn()
	}
	timers = nil
	w.reset()
	for i := range 40 { // a streaming tool pushing deltas far faster than 120 fps
		now = now.Add(200 * time.Microsecond)
		mu.Lock()
		lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(100), Live: &aria.Live{From: 0, V: i + 1, Nodes: []aria.NodeDelta{{ID: 1, Set: map[string]any{
			"type":     string(livedoc.NodeProse),
			"markdown": strings.Repeat("token ", i+1),
		}}}}}}}})
		mu.Unlock()
	}
	painted := w.writes.Load()
	if painted > 2 {
		t.Fatalf("40 stream deltas inside one frame interval painted %d frames, want <= 2", painted)
	}
	if len(timers) != 1 {
		t.Fatalf("armed %d trailing renders, want 1", len(timers))
	}
	now = now.Add(transcriptFrameInterval)
	timers[0]()
	if w.writes.Load() != painted+1 {
		t.Fatal("the trailing render did not paint the settled state")
	}
	if !strings.Contains(strings.Join(lt.tr.prev, "\n"), strings.Repeat("token ", 5)) {
		t.Fatal("the painted frame is not the latest stream state")
	}
	if lt.tr.dirty {
		t.Fatal("the trailing render left the screen dirty")
	}
}
