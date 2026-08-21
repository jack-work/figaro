package aria

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// CLIENT FOLD BENCHMARKS.
//
// These measure the paths the range store sits under, and they use nothing but
// the public API (Apply/View/Open/SetClosedLimit) so that the SAME FILE
// compiles and runs against the base commit. That is the point: the phase-1
// promise is that landing behind the existing API costs nothing, and a promise
// with no measurement is a wish.
//
// Apply is the per-frame path: one call per live delta, i.e. per few
// characters of streamed model output. View is the per-repaint path; the
// transcript calls it whenever ClosedRevision moves. Open is called on EVERY
// transcript frame.

func benchNode(tag string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: tag}
}

// sealedPage is one finished turn as it arrives from a catch-up read.
func sealedPage(turn int, nodes int) Page {
	t := Turn{ID: uint64(turn), Sealed: true, Inquiry: "why?"}
	for i := 0; i < nodes; i++ {
		t.Nodes = append(t.Nodes, benchNode(fmt.Sprintf("t%d.n%d", turn, i)))
	}
	return Page{Parts: []TurnPart{{Turn: t, From: 0}}}
}

// foldHistory drives n sealed turns through a fresh client, which is the
// insert/merge path: n messages folded one page at a time.
func foldHistory(n, nodes int) *Client {
	c := NewClient()
	for i := 1; i <= n; i++ {
		c.Apply(sealedPage(i, nodes))
	}
	return c
}

func BenchmarkClientFoldHistory(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(foldHistory(n, 1))
			}
		})
	}
}

func BenchmarkClientView(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			c := foldHistory(n, 1)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.KeepAlive(c.View())
			}
		})
	}
}

// BenchmarkClientApplyLiveDelta is the per-frame path: a client holding n
// turns of history takes one more live delta on the open turn.
func BenchmarkClientApplyLiveDelta(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			c := foldHistory(n, 1)
			id := uint64(n + 1)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: id, Live: &Live{
					From: 0, V: i + 1,
					Nodes: []NodeDelta{{ID: 0, Set: map[string]any{
						"type": "prose", "role": "output", "markdown": "streaming",
					}}},
				}}}}})
			}
		})
	}
}

// BenchmarkClientOpen is called on every transcript frame.
func BenchmarkClientOpen(b *testing.B) {
	c := foldHistory(1_000, 1)
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1001, Live: &Live{
		From: 0, V: 1,
		Nodes: []NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": "x"}}},
	}}}}})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.KeepAlive(c.Open())
	}
}

// BenchmarkClientTrim folds history against a retention limit, so every Apply
// past the limit also evicts. This is the path SetClosedLimit puts the
// transcript on (transcriptTailLimit).
func BenchmarkClientTrim(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := NewClient()
				c.SetClosedLimit(200)
				for t := 1; t <= n; t++ {
					c.Apply(sealedPage(t, 1))
				}
				runtime.KeepAlive(c)
			}
		})
	}
}

// BenchmarkClientTallTurn folds ONE turn that releases its head in slices as
// Live.From advances: the long-turn path, where the head range's To moves
// inside a streaming turn.
func BenchmarkClientTallTurn(b *testing.B) {
	for _, n := range []int{100, 1_000} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := NewClient()
				for k := 0; k < n; k++ {
					c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Live: &Live{
						From: uint64(k), V: k + 1,
						Nodes: []NodeDelta{{ID: uint64(k), Set: map[string]any{
							"type": "prose", "role": "output", "markdown": "node",
						}}},
					}}}}})
				}
				runtime.KeepAlive(c)
			}
		})
	}
}
