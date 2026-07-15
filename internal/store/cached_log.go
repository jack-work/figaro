package store

import "sync"

// cachedLog is a memoized read-through view over a Log[T]: it materializes
// the channel's entries in memory on open and serves reads from there,
// appending incrementally. The agent reads the whole IR every turn, so
// this keeps that hot path off the disk. One cachedLog is shared per
// (aria, channel) by the backend, so a reader (angelus.aria.read) sees
// the writer's (agent's) appends.
type cachedLog[T any] struct {
	inner Log[T]
	mu    sync.RWMutex
	rows  []Entry[T]
	byFK  map[uint64]int // FigaroLT -> row index
}

var _ Log[any] = (*cachedLog[any])(nil)

func newCachedLog[T any](inner Log[T]) *cachedLog[T] {
	c := &cachedLog[T]{inner: inner, byFK: map[uint64]int{}}
	for _, e := range inner.Read() {
		c.byFK[e.FigaroLT] = len(c.rows)
		c.rows = append(c.rows, e)
	}
	return c
}

func (c *cachedLog[T]) Read() []Entry[T] {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Entry[T], len(c.rows))
	copy(out, c.rows)
	return out
}

func (c *cachedLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if i, ok := c.byFK[figaroLT]; ok {
		return c.rows[i], true
	}
	return Entry[T]{}, false
}

func (c *cachedLog[T]) PeekTail() (Entry[T], bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.rows) == 0 {
		return Entry[T]{}, false
	}
	return c.rows[len(c.rows)-1], true
}

func (c *cachedLog[T]) ScanFromEnd(n int) []Entry[T] {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n <= 0 || len(c.rows) == 0 {
		return nil
	}
	out := make([]Entry[T], 0, n)
	for i := len(c.rows) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, c.rows[i])
	}
	return out
}

func (c *cachedLog[T]) ReadBefore(figaroLT uint64, n int) []Entry[T] {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n <= 0 || figaroLT == 0 || len(c.rows) == 0 {
		return nil
	}
	out := make([]Entry[T], 0, n)
	for i := len(c.rows) - 1; i >= 0 && len(out) < n; i-- {
		if c.rows[i].FigaroLT < figaroLT {
			out = append(out, c.rows[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (c *cachedLog[T]) Append(e Entry[T]) (Entry[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stamped, err := c.inner.Append(e)
	if err != nil {
		return Entry[T]{}, err
	}
	c.byFK[stamped.FigaroLT] = len(c.rows)
	c.rows = append(c.rows, stamped)
	return stamped, nil
}

func (c *cachedLog[T]) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inner.Clear(); err != nil {
		return err
	}
	c.rows = nil
	c.byFK = map[uint64]int{}
	return nil
}
