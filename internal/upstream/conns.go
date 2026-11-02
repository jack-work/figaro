package upstream

import (
	"context"
	"io"
	"net"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

type conn struct {
	net.Conn
	pool   *pool
	born   time.Time
	active atomic.Int32
	closed atomic.Bool
}

type pool struct {
	maxAge time.Duration
	sweep  time.Duration

	mu       sync.Mutex
	conns    map[*conn]struct{}
	sweeping bool
}

func newPool(maxAge, sweep time.Duration) *pool {
	if sweep <= 0 {
		sweep = time.Minute
	}
	return &pool{maxAge: maxAge, sweep: sweep, conns: map[*conn]struct{}{}}
}

func (p *pool) dialer(inner func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := inner(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tc := &conn{Conn: c, pool: p, born: time.Now()}
		p.add(tc)
		return tc, nil
	}
}

func (p *pool) add(c *conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns[c] = struct{}{}
	if p.maxAge > 0 && !p.sweeping {
		p.sweeping = true
		go p.sweepLoop()
	}
}

func (p *pool) remove(c *conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, c)
}

func (p *pool) live() []*conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*conn, 0, len(p.conns))
	for c := range p.conns {
		out = append(out, c)
	}
	return out
}

func (p *pool) sweepLoop() {
	tick := time.NewTicker(p.sweep)
	defer tick.Stop()
	for now := range tick.C {
		p.retireOld(now)
		p.mu.Lock()
		if len(p.conns) == 0 {
			p.sweeping = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}
}

func (p *pool) retireOld(now time.Time) {
	for _, c := range p.live() {
		if now.Sub(c.born) >= p.maxAge && c.active.Load() == 0 {
			c.Close()
		}
	}
}

func (c *conn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.pool.remove(c)
	}
	return c.Conn.Close()
}

func tracked(nc net.Conn) *conn {
	for nc != nil {
		switch v := nc.(type) {
		case *conn:
			return v
		case interface{ NetConn() net.Conn }:
			nc = v.NetConn()
		default:
			return nil
		}
	}
	return nil
}

type use struct {
	mu       sync.Mutex
	c        *conn
	released bool
}

func (u *use) got(info httptrace.GotConnInfo) {
	c := tracked(info.Conn)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.released {
		return
	}
	if u.c != nil {
		u.c.active.Add(-1)
	}
	u.c = c
	if c != nil {
		c.active.Add(1)
	}
}

func (u *use) release() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.released {
		return
	}
	u.released = true
	if u.c != nil {
		u.c.active.Add(-1)
	}
}

type usedBody struct {
	io.ReadCloser
	u *use
}

func (b *usedBody) Close() error {
	err := b.ReadCloser.Close()
	b.u.release()
	return err
}
