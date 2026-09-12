package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

// BROAD RANGES. A highlight over hundreds of nodes and thousands of rows must
// not make painting, motion or the coordinate quadratic. Three costs:
//
//	paint   one frame of the viewport with the wash over N rows
//	extend  one j when the range is already N rows
//	resolve the coordinate of a range over N nodes
//
// The frame is the viewport, not the range, so paint must be flat in N; the
// motion touches one line and the index; and the rune matcher runs over the
// two endpoint nodes only, never the nodes between.

func benchFixture(b *testing.B, nodes int) *transcript {
	b.Helper()
	parts := make([]aria.TurnPart, nodes)
	for i := range parts {
		lt := uint64(10 + 3*i)
		parts[i] = aria.TurnPart{Turn: aria.Turn{
			ID: uint64(i + 1), Sealed: true, LTs: []uint64{lt, lt + 1},
			Inquiry: fmt.Sprintf("question %04d", i+1),
			Nodes: []livedoc.Node{{
				Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: lt + 1, Block: 0}},
				Markdown: fmt.Sprintf("answer %04d: the quick brown fox jumps over the lazy dog, and then a second sentence so that the row wraps at this width, and a third one for good measure", i+1),
			}},
		}}
	}
	client := aria.NewClient()
	applyTail(client, readBefore(parts, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(80, 40), 80, 40, ldrender.NodeText{}, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	pageToFloor(tr, parts) // hold the whole conversation, not the tail window
	tr.buildIndex()
	tr.render()
	return tr
}

// spanAll anchors a highlight on the first node row and puts the cursor on
// the last.
func spanAll(tr *transcript) {
	tr.key('v')
	tr.key('g')
	tr.key('g')
	tr.key('V')
	tr.key('G')
}

func BenchmarkVisual_PaintFrameOverWash(b *testing.B) {
	defer term.SetColorMode(term.ColorAlways)()
	for _, n := range []int{10, 100, 400} {
		tr := benchFixture(b, n)
		spanAll(tr)
		tr.buildIndex()
		b.Run(fmt.Sprintf("nodes=%d/rows=%d", n, tr.index.total), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tr.rowBuf = tr.window(tr.offset, tr.offset+tr.h, tr.rowBuf)
			}
		})
	}
}

func BenchmarkVisual_ExtendByOneRow(b *testing.B) {
	defer term.SetColorMode(term.ColorAlways)()
	for _, n := range []int{10, 100, 400} {
		tr := benchFixture(b, n)
		spanAll(tr)
		tr.key('k') // room to go both ways
		b.Run(fmt.Sprintf("nodes=%d/rows=%d", n, tr.index.total), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if i%2 == 0 {
					tr.key('j')
				} else {
					tr.key('k')
				}
			}
		})
	}
}

func BenchmarkVisual_ResolveCoordinate(b *testing.B) {
	for _, n := range []int{10, 100, 400} {
		tr := benchFixture(b, n)
		spanAll(tr)
		if _, _, err := tr.visualRange(); err != "" {
			b.Fatalf("visualRange: %s", err)
		}
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := tr.visualRange(); err != "" {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkVisual_ScrollBaseline is j with no visual mode up: the cost the
// pager already pays per keystroke (index rebuild, frame), so ExtendByOneRow
// can be read as a delta over it.
func BenchmarkVisual_ScrollBaseline(b *testing.B) {
	defer term.SetColorMode(term.ColorAlways)()
	for _, n := range []int{10, 100, 400} {
		tr := benchFixture(b, n)
		tr.offset = 5
		b.Run(fmt.Sprintf("nodes=%d/rows=%d", n, tr.index.total), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if i%2 == 0 {
					tr.key('j')
				} else {
					tr.key('k')
				}
			}
		})
	}
}
