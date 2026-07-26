package cli

import (
	"io"
	"strings"
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/internal/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// The invariants this merge creates.
//
// Axis A made the frame O(viewport); axis C made the per-row cost ~free and
// moved the clip + gutter column into the row cache. Neither branch could pin
// the two properties that only hold once both are in:
//
//  1. a frame's cost does not grow with the retained window (A's structure),
//     even though each row it emits is C's zero-alloc cached row;
//  2. the row cache is selection-independent (C's cache shape + A's
//     decorate-at-paint-time), so moving or clearing a selection re-renders no
//     prose at all.
// ---------------------------------------------------------------------------

// countingView wraps ariaView and counts node renders, which is how a
// re-materialization of a cached message shows up from the outside.
type countingView struct {
	inner   *ariaView
	renders int
}

func (v *countingView) Render(n livedoc.Node, width, tick int) []string {
	v.renders++
	return v.inner.Render(n, width, tick)
}

func (v *countingView) RenderExpanded(n livedoc.Node, width, tick int, full bool) []string {
	v.renders++
	return v.inner.RenderExpanded(n, width, tick, full)
}

// TestMergedFrameCostIsViewportBounded is the headline: a frame materializes
// the viewport and nothing else, no matter how many rows are retained. The
// count is exact (len(rowBuf) is what render composed the screen from), so this
// pins the structure rather than a timing; the allocation comparison alongside
// it catches a regression that materializes lazily but still churns per
// retained row.
func TestMergedFrameCostIsViewportBounded(t *testing.T) {
	measure := func(messages int) (rows, total int, allocs float64) {
		tr, _ := mixedTranscript(t, io.Discard, 100, 24, messages)
		tr.scrollBy(-1)
		tr.lines() // warm the row cache and the index
		tr.offset = tr.index.total / 2
		tr.render()
		body := tr.h - 2 // scrolled away from live, so no padding row (see layout)
		if len(tr.rowBuf) != body {
			t.Fatalf("%d messages: frame materialized %d rows, want exactly the %d-row body",
				messages, len(tr.rowBuf), body)
		}
		i := 0
		allocs = testing.AllocsPerRun(50, func() {
			if i%2 == 0 {
				tr.scrollBy(-1)
			} else {
				tr.scrollBy(1)
			}
			i++
		})
		return len(tr.rowBuf), tr.index.total, allocs
	}
	smallRows, smallTotal, smallAllocs := measure(4)
	bigRows, bigTotal, bigAllocs := measure(24)
	if bigTotal < 3*smallTotal {
		t.Fatalf("fixture is not big enough to tell the difference: %d vs %d lines", smallTotal, bigTotal)
	}
	if smallRows != bigRows {
		t.Errorf("frame materialized %d rows over %d retained lines but %d rows over %d",
			smallRows, smallTotal, bigRows, bigTotal)
	}
	if smallRows*4 > smallTotal || bigRows*4 > bigTotal {
		t.Errorf("viewport (%d/%d rows) is not a small fraction of the window; the test proves nothing",
			bigRows, bigTotal)
	}
	// Generous slack: what must not happen is growth proportional to the
	// retained window (which would be ~6x here).
	if bigAllocs > smallAllocs+8 {
		t.Errorf("scroll allocations grew with the retained window: %v allocs over %d lines vs %v over %d",
			bigAllocs, bigTotal, smallAllocs, smallTotal)
	}
}

// TestMergedSelectionDoesNotInvalidateRowCache pins the decoration decision: the
// cue is applied to painted rows, never baked into rowCache, so selecting,
// extending and clearing a selection re-renders no nodes and leaves the cached
// row slices byte-identical (same backing array, same contents).
func TestMergedSelectionDoesNotInvalidateRowCache(t *testing.T) {
	view := &countingView{inner: &ariaView{settings: &renderSettings{}}}
	ft := ldrender.NewFakeTerminal(80, 24)
	tr, _ := mixedTranscript(t, ft, 80, 24, 6)
	tr.view = view
	tr.invalidateRows()
	tr.scrollBy(-1)
	tr.render() // warm every retained message through the counting view

	type snap struct {
		ptr  uintptr
		rows []string
	}
	before := map[sliceKey]snap{}
	for lt, cached := range tr.rowCache {
		s := snap{}
		if len(cached.rows) > 0 {
			s.ptr = uintptr(unsafe.Pointer(&cached.rows[0]))
		}
		for _, r := range cached.rows {
			s.rows = append(s.rows, r.text)
		}
		before[lt] = s
	}
	if len(before) == 0 {
		t.Fatal("row cache is empty; the fixture rendered nothing")
	}
	warm := view.renders

	// Walk a selection across message boundaries, extend it, expand nothing,
	// then clear it. None of that may re-render a node.
	for range 5 {
		tr.selectNode(1, false)
	}
	for range 3 {
		tr.selectNode(1, true)
	}
	painted := strings.Join(tr.lines(), "\n")
	tr.clearSelection()
	tr.render()

	if view.renders != warm {
		t.Errorf("selection movement re-rendered %d nodes; the row cache is not selection-independent",
			view.renders-warm)
	}
	for lt, s := range before {
		cached, ok := tr.rowCache[lt]
		if !ok {
			continue // legitimately dropped by clearSelection's trimPages
		}
		if len(cached.rows) > 0 && s.ptr != 0 && uintptr(unsafe.Pointer(&cached.rows[0])) != s.ptr {
			t.Errorf("LT %d: cached rows were reallocated", lt)
			continue
		}
		for i, r := range cached.rows {
			if i < len(s.rows) && r.text != s.rows[i] {
				t.Errorf("LT %d row %d: cached text changed under a selection:\n got %q\nwant %q",
					lt, i, r.text, s.rows[i])
				break
			}
		}
	}
	// ... and the cue really was painted, so the above is not vacuous.
	if !strings.Contains(painted, "▎") {
		t.Error("no selection cue in the painted rows")
	}
}

// TestMergedLinesBufferIsReused keeps C's contract explicit now that lines() is
// implemented on top of A's window: the whole-window materialization reuses its
// buffer (valid until the next call) and steady-state allocates nothing.
func TestMergedLinesBufferIsReused(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 6)
	tr.scrollBy(-1)
	first := tr.lines()
	firstPtr := &first[0]
	second := tr.lines()
	if &second[0] != firstPtr {
		t.Error("lines() allocated a fresh buffer instead of reusing t.lineBuf")
	}
	if got := testing.AllocsPerRun(20, func() { tr.lines() }); got != 0 {
		t.Errorf("steady-state lines() allocated %v times, want 0", got)
	}
	// The render path must not share that buffer: a frame composed after
	// lines() may not disturb the rows lines() handed back.
	rows := append([]string(nil), tr.lines()...)
	tr.render()
	current := tr.lines()
	for i := range rows {
		if rows[i] != current[i] {
			t.Fatalf("render() disturbed the lines() buffer at row %d", i)
		}
	}
}
