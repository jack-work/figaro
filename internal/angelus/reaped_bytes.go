package angelus

import (
	"log/slog"
	"sync"
)

type ownBytesBatcher interface {
	OwnBytesOf(ids []string) (map[string]int64, error)
}

type reapedBytes struct {
	mu     sync.Mutex
	byNode map[string]int64
}

func (a *Angelus) reapedGroupBytes(groups []reapedGroup) map[string]int64 {
	out := make(map[string]int64, len(groups))
	b, ok := a.Backend.(ownBytesBatcher)
	if !ok {
		return out
	}
	c := &a.reapedSizes
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byNode == nil {
		c.byNode = map[string]int64{}
	}
	present := map[string]bool{}
	var missing []string
	for _, g := range groups {
		for _, n := range g.nodes {
			present[n] = true
			if _, ok := c.byNode[n]; !ok {
				missing = append(missing, n)
			}
		}
	}
	if len(missing) > 0 {
		got, err := b.OwnBytesOf(missing)
		if err != nil {
			slog.Warn("reaped sizes not measured; the budget counts them next sweep", "nodes", len(missing), "err", err)
		}
		for n, size := range got {
			c.byNode[n] = size
		}
	}
	for n := range c.byNode {
		if !present[n] {
			delete(c.byNode, n)
		}
	}
	for _, g := range groups {
		for _, n := range g.nodes {
			out[g.root] += c.byNode[n]
		}
	}
	return out
}
