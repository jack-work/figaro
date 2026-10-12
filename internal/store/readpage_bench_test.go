package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// READPAGE, by the shapes its callers actually ask. The tail probe (0, ^0, 1)
// is what the dormant reader runs on every read of an aria and the agent runs
// on its own log; a tail page (0, ^0, 32) is a backward page of history; a
// forward page from the middle is the composed cache filling an LT bracket of
// an ancestor. raw is the node log the composed cache opens for an ancestor;
// aria is the aria's own log, the tree log in front of the same channel.
//
// Compiles against the tree before the positional ReadPage, so benchstat
// compares like with like.
func BenchmarkReadPage(b *testing.B) {
	prose := strings.Repeat("a sentence of ordinary length in a reply. ", 24) // ~1 KB
	for _, history := range []int{300, 3000} {
		be, id, log := newConversation(b)
		for t := uint64(1); t <= uint64(history/3); t++ {
			put(b, log, ask(t, "question"))
			put(b, log, answer(t, prose))
			put(b, log, answer(t, prose))
		}
		tail, _ := log.ReadPage(0, ^uint64(0), 1)
		mid := tail[0].LT / 2
		aria, _ := be.OpenFigIR(id)
		for _, src := range []struct {
			name string
			log  Log[message.Message]
		}{{"raw", be.OpenNode(id)}, {"aria", aria}} {
			for _, q := range []struct {
				name         string
				from, before uint64
				n            int
			}{
				{"tail-1", 0, ^uint64(0), 1},
				{"tail-32", 0, ^uint64(0), 32},
				{"forward-32", mid, 0, 32},
			} {
				b.Run(fmt.Sprintf("log=%s/query=%s/history=%d", src.name, q.name, history), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if page, _ := src.log.ReadPage(q.from, q.before, q.n); len(page) == 0 {
							b.Fatal("empty page")
						}
					}
				})
			}
		}
	}
}
