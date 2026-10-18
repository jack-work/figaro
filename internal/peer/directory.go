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
	Locate(ctx context.Context, id string) (node string, err error)
}

type Ask func(ctx context.Context, node string, ids []string) ([]string, error)

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
	node  string
	owns  bool
	unrch error
}

func (c *Cache) Locate(ctx context.Context, id string) (string, error) {
	if node, ok := c.Owner(id); ok {
		return node, nil
	}
	answers := make(chan claim, len(c.nodes))
	for _, node := range c.nodes {
		go func() {
			owned, err := c.ask(ctx, node, []string{id})
			answers <- claim{node: node, owns: err == nil && slices.Contains(owned, id), unrch: err}
		}()
	}
	var owners []string
	var unreachable []error
	for range c.nodes {
		a := <-answers
		switch {
		case a.unrch != nil:
			unreachable = append(unreachable, a.unrch)
		case a.owns:
			owners = append(owners, a.node)
		}
	}
	slices.Sort(owners)
	switch {
	case len(owners) == 1:
		c.Learn(id, owners[0])
		return owners[0], nil
	case len(owners) > 1:
		return "", fmt.Errorf("%s is on nodes %s: name it as <node>/%s", id, strings.Join(owners, " and "), id)
	case len(unreachable) > 0:
		return "", fmt.Errorf("%s: %w among reachable peers; %w", id, ErrNoOwner, errors.Join(unreachable...))
	}
	return "", fmt.Errorf("%s: %w", id, ErrNoOwner)
}
