package angelus

import (
	"fmt"
	"testing"
)

func benchTree(depth, width int) ([]fanoutNode, string) {
	nodes := []fanoutNode{{id: "root", live: true, quota: 1 << 20, capped: true}}
	parent := "root"
	for d := range depth {
		id := fmt.Sprintf("d%d", d)
		nodes = append(nodes, fanoutNode{id: id, parent: parent, live: true})
		parent = id
	}
	for i := range width {
		nodes = append(nodes, fanoutNode{id: fmt.Sprintf("w%d", i), parent: fmt.Sprintf("d%d", i%max(depth, 1)), live: i%3 != 0})
	}
	return nodes, parent
}

func BenchmarkFanoutAdmission(b *testing.B) {
	for _, depth := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("depth%d", depth), func(b *testing.B) {
			nodes, leaf := benchTree(depth, 2500)
			c := newFanoutCache()
			c.reset(nodes, nil)
			b.ResetTimer()
			for range b.N {
				if err := c.fits(leaf, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFanoutRebuild(b *testing.B) {
	nodes, _ := benchTree(32, 2511)
	c := newFanoutCache()
	b.ResetTimer()
	for range b.N {
		c.reset(nodes, nil)
	}
}
