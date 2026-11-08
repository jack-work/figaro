package cli

import "sync"

type settleGroup struct {
	mu   sync.Mutex
	n    int
	idle *sync.Cond
}

var settling = newSettleGroup()

func newSettleGroup() *settleGroup {
	g := &settleGroup{}
	g.idle = sync.NewCond(&g.mu)
	return g
}

func goSettled(f func()) {
	settling.mu.Lock()
	settling.n++
	settling.mu.Unlock()
	go func() {
		defer settling.leave()
		f()
	}()
}

func (g *settleGroup) leave() {
	g.mu.Lock()
	g.n--
	if g.n == 0 {
		g.idle.Broadcast()
	}
	g.mu.Unlock()
}

func (g *settleGroup) wait() {
	g.mu.Lock()
	for g.n > 0 {
		g.idle.Wait()
	}
	g.mu.Unlock()
}
