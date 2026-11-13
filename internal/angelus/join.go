package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

const shutdownJoinBound = 5 * time.Second

var errStopping = errors.New("daemon stopping")

type flight struct {
	name     string
	call     bool
	endpoint authz.Endpoint
	params   json.RawMessage
	listener io.Closer
}

func (f flight) describe() string {
	if !f.call {
		return f.name
	}
	aria := rpc.AriaOf(f.params)
	if aria == "" || rpc.ValidateAriaID(aria) != nil {
		aria = "-"
	}
	return f.name + " aria=" + aria + " endpoint=" + f.endpoint.String()
}

type inflight struct {
	sealed atomic.Bool

	mu    sync.Mutex
	seq   uint64
	out   map[uint64]flight
	empty chan struct{}
}

func (i *inflight) enter(f flight) (uint64, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sealed.Load() {
		return 0, false
	}
	if i.out == nil {
		i.out = map[uint64]flight{}
	}
	i.seq++
	i.out[i.seq] = f
	return i.seq, true
}

func (i *inflight) leave(id uint64) {
	i.mu.Lock()
	delete(i.out, id)
	if len(i.out) == 0 && i.empty != nil {
		close(i.empty)
		i.empty = nil
	}
	i.mu.Unlock()
}

func (i *inflight) seal() {
	i.mu.Lock()
	i.sealed.Store(true)
	var listeners []io.Closer
	for _, f := range i.out {
		if f.listener != nil {
			listeners = append(listeners, f.listener)
		}
	}
	i.mu.Unlock()
	for _, l := range listeners {
		_ = l.Close()
	}
}

func (i *inflight) names() []string {
	out := make([]string, 0, len(i.out))
	for _, f := range i.out {
		out = append(out, f.describe())
	}
	sort.Strings(out)
	return out
}

func (i *inflight) join(bound time.Duration) (waitedOn, stillOut []string) {
	i.mu.Lock()
	if len(i.out) == 0 {
		i.mu.Unlock()
		return nil, nil
	}
	waitedOn = i.names()
	empty := make(chan struct{})
	i.empty = empty
	i.mu.Unlock()

	t := time.NewTimer(bound)
	defer t.Stop()
	select {
	case <-empty:
		return waitedOn, nil
	case <-t.C:
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.empty = nil
	return waitedOn, i.names()
}

func (a *Angelus) Go(name string, fn func()) bool {
	id, ok := a.inflight.enter(flight{name: name})
	if !ok {
		return false
	}
	go func() {
		defer a.inflight.leave(id)
		fn()
	}()
	return true
}

func (a *Angelus) enterEndpoint(name string, ln net.Listener) (leave func(), ok bool) {
	id, ok := a.inflight.enter(flight{name: name, listener: ln})
	if !ok {
		ln.Close()
		return nil, false
	}
	return func() { a.inflight.leave(id) }, true
}

func (a *Angelus) stopping() bool { return a.inflight.sealed.Load() }

func (a *Angelus) refusingWhenStopping(next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if a.inflight.sealed.Load() {
			return nil, errStopping
		}
		return next(ctx, params)
	}
}

func (a *Angelus) joined(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		id, ok := a.inflight.enter(flight{name: method, call: true, endpoint: authz.EndpointOf(ctx), params: params})
		if !ok {
			return nil, errStopping
		}
		defer a.inflight.leave(id)
		return next(ctx, params)
	}
}
