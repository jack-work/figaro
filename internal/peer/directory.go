package peer

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
)

type Directory interface {
	Owner(id string) (node string, ok bool)
	Learn(id, node string)
	Forget(id string)
	Locate(ctx context.Context, ref string) (node, id string, err error)
}

type Ask func(ctx context.Context, node string, refs []string) (rpc.NodeResolveResponse, error)

var ErrNoOwner = errors.New("no node owns it")

type Cache struct {
	nodes []string
	ask   Ask
	size  int

	mu     sync.Mutex
	owners map[string]*list.Element
	recent *list.List
}

type owned struct {
	id, node string
}

func NewCache(nodes []string, ask Ask, size int) *Cache {
	return &Cache{nodes: slices.Clone(nodes), ask: ask, size: max(size, 1), owners: map[string]*list.Element{}, recent: list.New()}
}

func (c *Cache) Owner(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.owners[id]
	if !ok {
		return "", false
	}
	c.recent.MoveToFront(e)
	return e.Value.(*owned).node, true
}

func (c *Cache) Learn(id, node string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.owners[id]; ok {
		e.Value.(*owned).node = node
		c.recent.MoveToFront(e)
		return
	}
	c.owners[id] = c.recent.PushFront(&owned{id: id, node: node})
	for c.recent.Len() > c.size {
		last := c.recent.Back()
		c.recent.Remove(last)
		delete(c.owners, last.Value.(*owned).id)
	}
}

func (c *Cache) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.owners[id]; ok {
		c.recent.Remove(e)
		delete(c.owners, id)
	}
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recent.Len()
}

type claim struct {
	node  string
	id    string
	moved rpc.Moved
	err   error
}

func (c *Cache) Locate(ctx context.Context, ref string) (string, string, error) {
	if node, ok := c.Owner(ref); ok {
		return node, ref, nil
	}
	claims := make(chan claim, len(c.nodes))
	for _, node := range c.nodes {
		go func() {
			resp, err := c.ask(ctx, node, []string{ref})
			claims <- claim{node: node, id: resp.IDs[ref], moved: resp.Moved[ref], err: err}
		}()
	}
	var found, moved []claim
	var unreachable []error
	for range c.nodes {
		cl := <-claims
		switch {
		case cl.err != nil:
			unreachable = append(unreachable, cl.err)
		case cl.id != "":
			found = append(found, cl)
		case cl.moved.Node != "":
			moved = append(moved, cl)
		}
	}
	slices.SortFunc(found, func(a, b claim) int { return strings.Compare(a.node, b.node) })
	slices.SortFunc(moved, func(a, b claim) int { return strings.Compare(a.node, b.node) })
	switch {
	case len(found) == 1:
		c.Learn(found[0].id, found[0].node)
		return found[0].node, found[0].id, nil
	case len(found) > 1:
		nodes := make([]string, len(found))
		for i, f := range found {
			nodes[i] = f.node
		}
		return "", "", fmt.Errorf("%s is on nodes %s: name it as <node>/%s", ref, strings.Join(nodes, " and "), ref)
	case len(moved) > 0:
		m := moved[len(moved)-1].moved
		c.Learn(m.ID, m.Node)
		return m.Node, m.ID, nil
	case len(unreachable) > 0:
		return "", "", fmt.Errorf("%s is on no reachable node: %w", ref, errors.Join(unreachable...))
	}
	return "", "", fmt.Errorf("%s: %w", ref, ErrNoOwner)
}
