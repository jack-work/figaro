package angelus

import "sync"

type ownBytesBatcher interface {
	OwnBytesOf(ids []string) map[string]int64
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
		got := b.OwnBytesOf(missing)
		for _, n := range missing {
			c.byNode[n] = got[n]
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
