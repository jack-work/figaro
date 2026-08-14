// Command stormprobe is a HARNESS, not product code: it drives
// internal/livelog/aria.Server through the two shapes the process actually
// uses — the AGENT shape (OpenTurn → Update → Close → Seal(nil)) and the
// READER shape (Restore of turns composed with LT brackets) — and reports
// what the shared UIBudget knows about each.
//
// It exists to settle S1 (the legacyWhole latch) deterministically, in
// process, with no provider and no daemon: the storm proves it at scale,
// this proves it exactly.
//
//	go run ./scratch/stormprobe -turns 200 -kb 32
package main

import (
	"flag"
	"fmt"
	"runtime"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// body returns a DISTINCT string per turn. Sharing one string across turns
// makes every cache look free (they alias one allocation) — the first draft of
// this probe measured 0.23 MiB retained for 12.5 MiB of "composed" text.
func body(kb, turn int) string {
	b := make([]byte, kb*1024)
	for i := range b {
		b[i] = byte('a' + (i+turn)%26)
	}
	return string(b)
}

func nodes(prose, tool string) []livedoc.Node {
	return []livedoc.Node{
		{Type: livedoc.NodeProse, Markdown: prose},
		{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: tool},
	}
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// agentShape is exactly what internal/figaro/agent.go does per turn:
// aria/server.go OpenTurn appends Turn{ID: id} — no LTs — and
// agent.go finishTurn calls Seal(nil).
func agentShape(turns, kb int, budget *aria.UIBudget) (*aria.Server, uint64) {
	srv := aria.NewServer()
	srv.BindCache(func(from, to uint64) []aria.Turn { return nil }, budget)
	for i := 1; i <= turns; i++ {
		p, t := body(kb, i), body(kb, i+1<<20)
		srv.OpenTurn(uint64(i))
		srv.OpenInquiry(uint64(i), fmt.Sprintf("question %d", i))
		srv.Update(nodes(p, t))
		srv.Close()
		srv.Seal(nil) // <- agent.go:1217
	}
	return srv, heapNow()
}

// readerShape is what compose.Turns + AriaReader.Restore produce: every turn
// carries LTs{first,last}, which is the only reason the accountant sees it.
func readerShape(turns, kb int, budget *aria.UIBudget) (*aria.Server, uint64) {
	srv := aria.NewServer()
	srv.BindCache(func(from, to uint64) []aria.Turn { return nil }, budget)
	all := make([]aria.Turn, 0, turns)
	for i := 1; i <= turns; i++ {
		p, t := body(kb, i), body(kb, i+1<<20)
		all = append(all, aria.Turn{
			ID: uint64(i), Inquiry: fmt.Sprintf("question %d", i),
			LTs: []uint64{uint64(2*i - 1), uint64(2 * i)}, Sealed: true,
			Nodes: nodes(p, t),
		})
	}
	srv.Restore(all)
	return srv, heapNow()
}

func report(tag string, b *aria.UIBudget, heapBefore, heapAfter uint64) {
	res, lim, ev := b.Stats()
	fmt.Printf("%-8s budget: resident=%7.2f MiB  limit=%6.2f MiB  evictions=%-5d | heap retained=%7.2f MiB\n",
		tag, mib(uint64(res)), mib(uint64(lim)), ev, mib(heapAfter-heapBefore))
}

func main() {
	turns := flag.Int("turns", 200, "sealed turns per aria")
	kb := flag.Int("kb", 32, "KiB of composed text per node (x2 nodes per turn)")
	limit := flag.Int("limit-mb", 16, "UIBudget limit, MiB (config.toml ships 16)")
	flag.Parse()

	fmt.Printf("stormprobe: %d turns x %d KiB x 2 nodes = %.1f MiB composed, budget %d MiB\n\n",
		*turns, *kb, float64(*turns**kb*2)/1024, *limit)

	base := heapNow()
	bA := aria.NewUIBudget(*limit)
	srvA, hA := agentShape(*turns, *kb, bA)
	report("AGENT", bA, base, hA)

	base2 := heapNow()
	bR := aria.NewUIBudget(*limit)
	srvR, hR := readerShape(*turns, *kb, bR)
	report("READER", bR, base2, hR)

	runtime.KeepAlive(srvA)
	runtime.KeepAlive(srvR)

	fmt.Println()
	for _, r := range []int{10, 20, 40, 80} {
		churn := roundsShape(r, 4)
		fmt.Printf("SIZING  a %2d-round turn of 4 KiB nodes allocated %6.2f MiB of garbage (%.1f KiB per round-node-KiB)\n",
			r, mib(churn), float64(churn)/float64(r*4)/1024)
	}

	fmt.Println()
	fmt.Println("AGENT resident=0 with evictions=0 is the latch: turncache.go noteLegacy")
	fmt.Println("sees len(LTs)<2 on the first turn, sets legacyWhole for the WHOLE cache,")
	fmt.Println("and account() early-returns forever after — nothing is counted, nothing")
	fmt.Println("can be evicted, and `doctor mem` reports a ui window of zero while the")
	fmt.Println("aria retains every composed turn it ever ran.")
}

// roundsShape measures the SIZING churn: a turn that streams in R rounds
// calls Close (and thus TailMutated → turnBytes → nodeSize → json.Marshal of
// every node in the turn so far) once per round, so the marshal bill over a
// turn is quadratic in its length. Reported as TotalAlloc, which counts
// garbage rather than retention.
func roundsShape(rounds, kb int) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	srv := aria.NewServer()
	srv.BindCache(func(from, to uint64) []aria.Turn { return nil }, aria.NewUIBudget(16))
	var all []livedoc.Node
	srv.OpenTurn(1)
	for r := 1; r <= rounds; r++ {
		all = append(all, nodes(body(kb, r), body(kb, r+1<<20))...)
		srv.Update(all)
		srv.Close()     // folds the suffix in, calls TailMutated
		srv.OpenTurn(1) // the next round of the SAME turn
	}
	srv.Seal(nil)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(srv)
	return after.TotalAlloc - before.TotalAlloc
}
