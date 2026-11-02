package upstream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	ReasonAge      = "age"
	ReasonPeer     = "peer closed"
	ReasonError    = "error"
	ReasonInFlight = "in flight"
	ReasonIdle     = "idle"
)

type conn struct {
	net.Conn
	pool    *pool
	born    time.Time
	active  atomic.Int32
	lost    atomic.Bool
	served  atomic.Int64
	closed  atomic.Bool
	reason  atomic.Pointer[string]
	failure atomic.Pointer[error]
}

type Stats struct {
	Open    int               `json:"open"`
	Dropped map[string]uint64 `json:"dropped,omitempty"`
}

type pool struct {
	maxAge    time.Duration
	sweep     time.Duration
	watch     func(changed func()) error
	watchOnce sync.Once
	source    func(remote *net.TCPAddr) (net.IP, error)
	closeIdle func()

	mu       sync.Mutex
	conns    map[*conn]struct{}
	dropped  map[string]uint64
	sweeping bool
}

func newPool(cfg Config) *pool {
	p := &pool{maxAge: cfg.MaxAge, sweep: cfg.Sweep, source: kernelSource,
		conns: map[*conn]struct{}{}, dropped: map[string]uint64{}}
	if p.sweep <= 0 {
		p.sweep = time.Minute
	}
	if cfg.WatchNetwork {
		p.watch = watchRoutes
	}
	return p
}

func (p *pool) dialer(inner func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := inner(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tc := &conn{Conn: c, pool: p, born: time.Now()}
		p.add(tc)
		p.watchNetwork()
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

func (p *pool) remove(c *conn, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, c)
	p.dropped[reason]++
}

func (p *pool) stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Stats{Open: len(p.conns), Dropped: make(map[string]uint64, len(p.dropped))}
	for r, n := range p.dropped {
		s.Dropped[r] = n
	}
	return s
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
	var old []*conn
	for _, c := range p.live() {
		if now.Sub(c.born) >= p.maxAge && c.served.Load() > 0 && c.active.Load() == 0 {
			old = append(old, c)
		}
	}
	if len(old) == 0 || p.closeIdle == nil {
		return
	}
	age := ReasonAge
	for _, c := range old {
		c.reason.CompareAndSwap(nil, &age)
	}
	p.closeIdle()
	for _, c := range old {
		c.reason.CompareAndSwap(&age, nil)
	}
}

func (c *conn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		c.failed(err)
	}
	return n, err
}

func (c *conn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err != nil {
		c.failed(err)
	}
	return n, err
}

func (c *conn) failed(err error) {
	if !c.closed.Load() {
		c.failure.CompareAndSwap(nil, &err)
	}
}

func (c *conn) retire(reason string) {
	c.reason.CompareAndSwap(nil, &reason)
	c.Close()
}

func (c *conn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		reason := c.dropReason()
		c.pool.remove(c, reason)
		slog.Info("provider connection dropped", "reason", reason,
			"remote", c.RemoteAddr().String(), "local", c.LocalAddr().String(),
			"age", time.Since(c.born).Round(time.Second), "requests", c.served.Load())
	}
	return c.Conn.Close()
}

func (c *conn) dropReason() string {
	if r := c.reason.Load(); r != nil {
		return *r
	}
	if f := c.failure.Load(); f != nil {
		if errors.Is(*f, io.EOF) || errors.Is(*f, syscall.ECONNRESET) {
			return ReasonPeer
		}
		return ReasonError
	}
	if c.active.Load() > 0 || c.lost.Load() {
		return ReasonInFlight
	}
	return ReasonIdle
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
		c.served.Add(1)
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

func (u *use) failed(err error) {
	if err == nil || err == io.EOF || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrStalled) {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.c != nil {
		u.c.lost.Store(true)
	}
}

type usedBody struct {
	io.ReadCloser
	u *use
}

func (b *usedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.u.failed(err)
	return n, err
}

func (b *usedBody) Close() error {
	err := b.ReadCloser.Close()
	b.u.release()
	return err
}
