package angelus

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

func pct(ds []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

func timeN(n int, fn func()) []time.Duration {
	out := make([]time.Duration, n)
	for i := range n {
		t0 := time.Now()
		fn()
		out[i] = time.Since(t0)
	}
	return out
}

func report(t *testing.T, name string, ds []time.Duration) {
	t.Logf("%-44s n=%-4d p50=%-12v p90=%-12v max=%v", name, len(ds), pct(ds, .5), pct(ds, .9), pct(ds, 1))
}

// Measures today's fork admission on a store snapshot; FA_STORE names the copy.
func TestForkAdmissionOnSnapshot(t *testing.T) {
	root := os.Getenv("FA_STORE")
	if root == "" {
		t.Skip("FA_STORE unset")
	}
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Registry: NewRegistry(), Backend: b, Hubs: newHubs()}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	h := hs.h

	convs := b.ConversationIDs()
	nodes := b.Nodes()
	t.Logf("store: %d nodes, %d conversations", len(nodes), len(convs))

	t0 := time.Now()
	kids := map[string]int{}
	for _, id := range convs {
		if p, ok := a.topology().Drawn(id); ok {
			kids[p]++
		}
	}
	t.Logf("one full parent scan over conversations: %v", time.Since(t0))
	wide, wideN := "", 0
	for p, n := range kids {
		if n > wideN && p != "" {
			if nv, ok := b.Node(p); ok && nv.Kind == store.KindConversation {
				wide, wideN = p, n
			}
		}
	}
	t.Logf("widest conversation parent %s with %d drawn children", wide, wideN)
	depth := 0
	if ch, err := a.chainOf(wide); err == nil {
		depth = len(ch)
	}
	t.Logf("chain length of the widest parent: %d", depth)

	report(t, "topology().Children(wide)", timeN(50, func() { a.topology().Children(wide) }))
	report(t, "liveChildrenOf(wide)", timeN(50, func() { a.liveChildrenOf(wide) }))
	report(t, "fanoutLimit(wide) uncapped", timeN(200, func() { _, _, _ = a.fanoutLimit(wide) }))
	report(t, "admitFork(wide) uncapped", timeN(200, func() { _ = h.admitFork(wide) }))
	report(t, "chainOf(wide)", timeN(200, func() { _, _ = a.chainOf(wide) }))

	fork := func() {
		if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: wide}); err != nil {
			t.Fatal(err)
		}
	}
	report(t, "fork RPC (head, uncapped parent)", timeN(20, fork))
	report(t, "ForkWith alone (store birth)", timeN(20, func() {
		if _, _, err := b.ForkWith(wide, 0, *fanoutPatch(0)); err != nil {
			t.Fatal(err)
		}
	}))
	setMaxChildren(t, b, wide, 100000)
	t.Logf("live children now: %d", len(a.liveChildrenOf(wide)))
	report(t, "admitFork(wide) capped", timeN(50, func() { _ = h.admitFork(wide) }))
	report(t, "fork RPC (head, capped parent)", timeN(20, fork))
	for _, n := range []int{1, 4, 8} {
		var wg sync.WaitGroup
		t0 := time.Now()
		for range n {
			wg.Add(1)
			go func() { defer wg.Done(); fork() }()
		}
		wg.Wait()
		t.Logf("%d concurrent forks under one parent: wall %v (%v each)", n, time.Since(t0), time.Since(t0)/time.Duration(n))
	}
	other := convs[len(convs)-1]
	for _, n := range []int{1, 8} {
		var wg sync.WaitGroup
		t0 := time.Now()
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, _ = b.ForkWith(wide, 0, *fanoutPatch(0))
			}()
		}
		wg.Wait()
		t.Logf("%d concurrent store births, no hierarchy lock: wall %v", n, time.Since(t0))
	}
	report(t, "hierarchy WithLock, uncontended", timeN(200, func() { _ = a.topology().WithLock(func() error { return nil }) }))
	if other == wide {
		other = convs[0]
	}
	var wg sync.WaitGroup
	t0 = time.Now()
	for i := range 8 {
		wg.Add(1)
		id := wide
		if i%2 == 1 {
			id = other
		}
		go func() {
			defer wg.Done()
			_, _ = callFork(t, hs, rpc.ForkRequest{FigaroID: id})
		}()
	}
	wg.Wait()
	t.Logf("8 concurrent forks over two unrelated parents: wall %v", time.Since(t0))
}
