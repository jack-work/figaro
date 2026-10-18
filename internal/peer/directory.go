package peer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

type Directory interface {
	Owner(id string) (node string, ok bool)
	Learn(id, node string)
	Forget(id string)
	Locate(ctx context.Context, ref string) (node, id string, err error)
}

type Ask func(ctx context.Context, node string, refs []string) (map[string]string, error)

var ErrNoOwner = errors.New("no node owns it")

type Cache struct {
	nodes []string
	ask   Ask

	mu     sync.RWMutex
	owners map[string]string
}

func NewCache(nodes []string, ask Ask) *Cache {
	return &Cache{nodes: slices.Clone(nodes), ask: ask, owners: map[string]string{}}
}

func (c *Cache) Owner(id string) (string, bool) {
	c.mu.RLock()
	node, ok := c.owners[id]
	c.mu.RUnlock()
	return node, ok
}

func (c *Cache) Learn(id, node string) {
	c.mu.Lock()
	c.owners[id] = node
	c.mu.Unlock()
}

func (c *Cache) Forget(id string) {
	c.mu.Lock()
	delete(c.owners, id)
	c.mu.Unlock()
}

type claim struct {
	node string
	id   string
	err  error
}

func (c *Cache) Locate(ctx context.Context, ref string) (string, string, error) {
	if node, ok := c.Owner(ref); ok {
		return node, ref, nil
	}
	claims := make(chan claim, len(c.nodes))
	for _, node := range c.nodes {
		go func() {
			ids, err := c.ask(ctx, node, []string{ref})
			claims <- claim{node: node, id: ids[ref], err: err}
		}()
	}
	var found []claim
	var unreachable []error
	for range c.nodes {
		cl := <-claims
		switch {
		case cl.err != nil:
			unreachable = append(unreachable, cl.err)
		case cl.id != "":
			found = append(found, cl)
		}
	}
	slices.SortFunc(found, func(a, b claim) int { return strings.Compare(a.node, b.node) })
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
	case len(unreachable) > 0:
		return "", "", fmt.Errorf("%s is on no reachable node: %w", ref, errors.Join(unreachable...))
	}
	return "", "", fmt.Errorf("%s: %w", ref, ErrNoOwner)
}
