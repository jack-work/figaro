package aria

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func benchNode(tag string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: tag}
}

func sealedPage(turn int, nodes int) Page {
	t := Turn{ID: uint64(turn), Sealed: true, Inquiry: "why?"}
	for i := 0; i < nodes; i++ {
		t.Nodes = append(t.Nodes, benchNode(fmt.Sprintf("t%d.n%d", turn, i)))
	}
	return Page{Parts: []TurnPart{{Turn: t, From: 0}}}
}

// foldHistory drives n sealed turns through a fresh client, one page at a time.
func foldHistory(n, nodes int) *Client {
	c := NewClient()
	for i := 1; i <= n; i++ {
		c.Apply(sealedPage(i, nodes), Notify)
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
				}}}}}, Notify)
			}
		})
	}
}

func BenchmarkClientOpen(b *testing.B) {
	c := foldHistory(1_000, 1)
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1001, Live: &Live{
		From: 0, V: 1,
		Nodes: []NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": "x"}}},
	}}}}}, Notify)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.KeepAlive(c.Open())
	}
}

// BenchmarkClientTrim folds history against a retention limit, so every Apply
// past the limit also evicts.
func BenchmarkClientTrim(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := NewClient()
				c.SetClosedLimit(200)
				for t := 1; t <= n; t++ {
					c.Apply(sealedPage(t, 1), Notify)
				}
				runtime.KeepAlive(c)
			}
		})
	}
}

// BenchmarkClientTallTurn folds one turn that releases its head in slices as
// Live.From advances.
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
					}}}}}, Notify)
				}
				runtime.KeepAlive(c)
			}
		})
	}
}
