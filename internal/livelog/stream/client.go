package stream

import (
	"sync"

	"github.com/jack-work/figaro/internal/livelog/doc"
)

// Client maintains a local Doc synced from a Feed. Lifecycle:
//
//	Follow(feed)   — (optional) subscribe to live events first, so nothing
//	                 published between catch-up and subscribe is missed
//	Catchup(feed)  — rebuild from the snapshot (first time) or recover the gap
//	                 from the tail (reconnect); idempotent
//	(disconnect)   — the live cancel fires; on reconnect call Catchup again
//
// Every application is idempotent by sequence number and the first catch-up
// rebuilds from the authoritative snapshot, so any order of Follow/Catchup and
// any catch-up/live overlap converges to the server's state. A mutex serializes
// live callbacks against catch-up. OnUpdate fires after any change.
type Client struct {
	mu          sync.Mutex
	d           *doc.Doc
	cursor      int // highest applied Seq
	limit       int // page size
	snapshotted bool

	OnUpdate func(*doc.Doc)
}

// NewClient returns a fresh client with the given catch-up page size.
func NewClient(pageSize int) *Client {
	if pageSize <= 0 {
		pageSize = 64
	}
	return &Client{d: doc.New(), limit: pageSize}
}

// Doc returns the local document. Not safe to read concurrently with sync; copy
// via Doc().Blocks() under the same goroutine as OnUpdate.
func (c *Client) Doc() *doc.Doc {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.d
}

// Cursor is the highest applied sequence number.
func (c *Client) Cursor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursor
}

// Catchup brings the local Doc current with feed. The first call rebuilds from
// the paginated snapshot (authoritative — it resets any partial state); later
// calls page only the tail from the cursor, recovering a post-disconnect gap.
func (c *Client) Catchup(feed Feed) {
	c.mu.Lock()
	if !c.snapshotted {
		c.d = doc.New() // the snapshot is authoritative; discard partial state
		c.cursor = 0
		offset := 0
		for {
			p := feed.Snapshot(offset, c.limit)
			for _, b := range p.Blocks {
				c.d.Apply(doc.Append(b))
			}
			offset += len(p.Blocks)
			c.cursor = p.Head
			if !p.HasMore() {
				break
			}
		}
		c.snapshotted = true
	}
	for {
		p := feed.Tail(c.cursor, c.limit)
		for _, e := range p.Events {
			c.apply(e)
		}
		if !p.HasMore {
			break
		}
	}
	c.mu.Unlock()
	c.fire()
}

// Follow subscribes for live events (deduped by Seq). Returns the unsubscribe
// func; after calling it (a disconnect) call Catchup to recover the gap.
func (c *Client) Follow(feed Feed) (cancel func()) {
	return feed.Subscribe(func(e doc.Event) {
		c.mu.Lock()
		changed := c.apply(e)
		c.mu.Unlock()
		if changed {
			c.fire()
		}
	})
}

// apply folds e if it advances the cursor; caller holds c.mu.
func (c *Client) apply(e doc.Event) bool {
	if e.Seq <= c.cursor {
		return false
	}
	c.d.Apply(e)
	c.cursor = e.Seq
	return true
}

func (c *Client) fire() {
	if c.OnUpdate == nil {
		return
	}
	c.mu.Lock()
	d := c.d
	c.mu.Unlock()
	c.OnUpdate(d)
}
