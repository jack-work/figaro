package angelus

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

type fanoutNode struct {
	id, parent string
	live       bool
}

type fanoutCache struct {
	mu     sync.Mutex
	parent map[string]string
	live   map[string]bool
	kids   map[string]int
}

func newFanoutCache() *fanoutCache {
	return &fanoutCache{parent: map[string]string{}, live: map[string]bool{}, kids: map[string]int{}}
}

func (c *fanoutCache) reset(nodes []fanoutNode) {
	parent := make(map[string]string, len(nodes))
	live := make(map[string]bool, len(nodes))
	kids := map[string]int{}
	for _, n := range nodes {
		parent[n.id], live[n.id] = n.parent, n.live
		if n.live && n.parent != "" {
			kids[n.parent]++
		}
	}
	c.mu.Lock()
	c.parent, c.live, c.kids = parent, live, kids
	c.mu.Unlock()
}

func (c *fanoutCache) put(n fanoutNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(n.id)
	c.parent[n.id], c.live[n.id] = n.parent, n.live
	if n.live && n.parent != "" {
		c.kids[n.parent]++
	}
}

func (c *fanoutCache) setLive(id string, live bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, known := c.parent[id]
	if !known || c.live[id] == live {
		return
	}
	c.live[id] = live
	if p == "" {
		return
	}
	if live {
		c.kids[p]++
	} else {
		c.kids[p]--
	}
}

func (c *fanoutCache) move(id, parent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	from, known := c.parent[id]
	if !known || from == parent {
		return
	}
	if c.live[id] {
		if from != "" {
			c.kids[from]--
		}
		if parent != "" {
			c.kids[parent]++
		}
	}
	c.parent[id] = parent
}

func (c *fanoutCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(id)
}

func (c *fanoutCache) dropLocked(id string) {
	p, known := c.parent[id]
	if !known {
		return
	}
	if c.live[id] && p != "" {
		c.kids[p]--
	}
	delete(c.parent, id)
	delete(c.live, id)
}

func (c *fanoutCache) liveChildren(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kids[id]
}

func (c *fanoutCache) drift(nodes []fanoutNode) []rpc.FanoutDrift {
	tree := newFanoutCache()
	tree.reset(nodes)
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []rpc.FanoutDrift
	seen := map[string]bool{}
	note := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		cp, cknown := c.parent[id]
		tp, tknown := tree.parent[id]
		if cknown == tknown && cp == tp && c.live[id] == tree.live[id] && c.kids[id] == tree.kids[id] {
			return
		}
		out = append(out, rpc.FanoutDrift{ID: id, Cached: describeFanout(cknown, cp, c.live[id], c.kids[id]), Tree: describeFanout(tknown, tp, tree.live[id], tree.kids[id])})
	}
	for id := range tree.parent {
		note(id)
	}
	for id := range c.parent {
		note(id)
	}
	for id, n := range c.kids {
		if n != 0 {
			note(id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func describeFanout(known bool, parent string, live bool, kids int) string {
	if !known {
		return fmt.Sprintf("absent, %d live children", kids)
	}
	return fmt.Sprintf("under %q, live %t, %d live children", parent, live, kids)
}

func (a *Angelus) fanoutCounts() *fanoutCache {
	a.fanoutOnce.Do(func() {
		c := newFanoutCache()
		a.fanout = c
		if a.Backend == nil {
			return
		}
		a.Backend.Feed(store.TopologyFeed{
			Born:     func(id, parent string) { c.put(fanoutNode{id: id, parent: parent, live: a.isLiveAria(id)}) },
			Liveness: c.setLive,
		})
		c.reset(a.fanoutNodes())
	})
	return a.fanout
}

func (a *Angelus) fanoutNodes() []fanoutNode {
	over := a.Backend.HierarchyEngine().Overrides()
	views := a.Backend.EveryNode()
	out := make([]fanoutNode, 0, len(views))
	for _, v := range views {
		parent := v.Parent
		if p := over[v.ID]; p != "" {
			parent = p
		}
		out = append(out, fanoutNode{id: v.ID, parent: parent, live: a.isLiveAria(v.ID)})
	}
	return out
}

func (a *Angelus) liveChildCount(id string) int {
	return a.fanoutCounts().liveChildren(id)
}

func (a *Angelus) settleFanout(p hierarchy.Plan) {
	c := a.fanoutCounts()
	for _, m := range p.Moves {
		c.move(m.Node, m.To)
	}
	for _, id := range p.Delete {
		if a.nodeKept(id) {
			c.setLive(id, false)
			continue
		}
		c.forget(id)
	}
	for _, id := range p.Revive {
		c.put(fanoutNode{id: id, parent: a.drawnParentOf(id), live: a.isLiveAria(id)})
	}
}

func (a *Angelus) nodeKept(id string) bool {
	if a.Backend == nil {
		return false
	}
	_, ok := a.Backend.Node(id)
	return ok
}

func (a *Angelus) drawnParentOf(id string) string {
	if p := a.Backend.HierarchyEngine().Overrides()[id]; p != "" {
		return p
	}
	v, _ := a.Backend.Node(id)
	return v.Parent
}

func (a *Angelus) fanoutDrift() []rpc.FanoutDrift {
	if a.Backend == nil {
		return nil
	}
	var out []rpc.FanoutDrift
	_ = a.topology().WithLock(func() error {
		out = a.fanoutCounts().drift(a.fanoutNodes())
		return nil
	})
	return out
}

func (a *Angelus) reconcileFanout() {
	if a.Backend == nil {
		return
	}
	_ = a.topology().WithLock(func() error {
		nodes := a.fanoutNodes()
		c := a.fanoutCounts()
		if d := c.drift(nodes); len(d) > 0 {
			slog.Warn("fanout: the cached counts drifted from the tree and were rebuilt", "nodes", len(d), "first", d[0].ID)
			c.reset(nodes)
		}
		return nil
	})
}
