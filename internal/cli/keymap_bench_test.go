package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// The per-keystroke cost of dispatch.
//
// The transcript's input path is coalesced and frame-paced; the one thing a
// table lookup could plausibly cost is per-key time and per-key allocation.
// These two benchmarks measure exactly that, with the paint held off by a
// batch, so a regression cannot hide behind the painter.
// ---------------------------------------------------------------------------

// BenchmarkKeyDispatchPager: one motion key through the pager's dispatcher,
// inside a batch so no frame is painted.
func BenchmarkKeyDispatchPager(b *testing.B) {
	var w countingWriter
	committed := make([]aria.TurnPart, 40)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 20)}}
	}
	settings := &renderSettings{}
	status := newSessionStatus("aria0001", time.Unix(0, 0))
	lt := newLivelogTurn(&w, 100, 40, settings, "aria0001", time.Unix(0, 0), status, nil, nil)
	lt.enterTranscript()
	lt.apply(aria.Page{Parts: committed})
	tr := lt.tr
	tr.beginBatch()
	defer tr.endBatch()
	tr.render() // warm the index and the row cache

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%2 == 0 {
			tr.key('j')
		} else {
			tr.key('k')
		}
	}
}

// BenchmarkKeyDispatchInput: a whole read chunk of keystrokes through the
// input loop: decode, opener gate, lookup, act, as one frame. 256 keys per
// chunk, so the per-key cost dominates the single paint at the end.
func BenchmarkKeyDispatchInput(b *testing.B) {
	var w countingWriter
	in, _ := coalesceInput(b, &w)
	chunk := []byte(strings.Repeat("jk", 128))

	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for range b.N {
		if rest, stop := in.consume(chunk); stop || len(rest) != 0 {
			b.Fatalf("consume = %q, %v", rest, stop)
		}
	}
}

// BenchmarkKeyDispatchArrows: the same chunk as escape sequences, which take
// the longer decode path (delimit, classify, nav lookup).
func BenchmarkKeyDispatchArrows(b *testing.B) {
	var w countingWriter
	in, _ := coalesceInput(b, &w)
	chunk := []byte(strings.Repeat("\x1b[A\x1b[B", 128))

	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for range b.N {
		if rest, stop := in.consume(chunk); stop || len(rest) != 0 {
			b.Fatalf("consume = %q, %v", rest, stop)
		}
	}
}
