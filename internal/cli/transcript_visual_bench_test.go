package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// Paint is flat in the range size, motion touches one line, and the rune
// matcher runs over the two endpoint nodes only.

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
	pageToFloor(tr, parts)
	tr.buildIndex()
	tr.render()
	return tr
}

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
		tr.key('k')
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
