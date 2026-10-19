package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/jkrpc"
)

type follower struct {
	srv *jkrpc.Server

	mu     sync.Mutex
	hubs   map[*ariaHub]struct{}
	links  map[string]*peer.Link
	closed bool
}

type followerKey struct{}

func followerOf(ctx context.Context) *follower {
	f, _ := ctx.Value(followerKey{}).(*follower)
	return f
}

func (f *follower) notify(method string, params json.RawMessage) error {
	return f.srv.Notify(method, params)
}

func (f *follower) follow(hb *ariaHub) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if _, ok := f.hubs[hb]; ok {
		f.mu.Unlock()
		return
	}
	f.hubs[hb] = struct{}{}
	f.mu.Unlock()
	if !hb.add(f) {
		f.forget(hb)
	}
}

func (f *follower) forget(hb *ariaHub) {
	f.mu.Lock()
	delete(f.hubs, hb)
	f.mu.Unlock()
}

func (f *follower) close() {
	f.mu.Lock()
	f.closed = true
	hubs := make([]*ariaHub, 0, len(f.hubs))
	for hb := range f.hubs {
		hubs = append(hubs, hb)
	}
	links := f.links
	f.hubs, f.links = nil, nil
	f.mu.Unlock()
	for _, hb := range hubs {
		hb.remove(f)
	}
	for _, l := range links {
		l.Close()
	}
}

func (f *follower) link(ctx context.Context, d *peer.Dialer, node string, p rpc.Passage) (*peer.Link, error) {
	f.mu.Lock()
	l, ok := f.links[node]
	f.mu.Unlock()
	if ok {
		select {
		case <-l.Done():
			l.Close()
		default:
			return l, nil
		}
	}
	fresh, err := d.Open(ctx, node, p, func(method string, params json.RawMessage) {
		f.notify(method, params)
	})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		fresh.Close()
		return nil, fmt.Errorf("connection closed")
	}
	if cur, ok := f.links[node]; ok && cur != l {
		fresh.Close()
		return cur, nil
	}
	if f.links == nil {
		f.links = map[string]*peer.Link{}
	}
	f.links[node] = fresh
	return fresh, nil
}

type Admit func(ctx context.Context, conn net.Conn) (context.Context, error)

func (a *Angelus) cliAdmit() Admit {
	if a.Peercred != nil {
		return a.Peercred.Admit
	}
	return authz.Admit
}

func (a *Angelus) Serve(ctx context.Context, ln net.Listener, admit Admit) error {
	if admit == nil {
		admit = authz.Admit
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			slog.Warn("door accept", "err", err)
			continue
		}
		go a.admitConn(ctx, conn, admit)
	}
}

func (a *Angelus) admitConn(ctx context.Context, conn net.Conn, admit Admit) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cctx, err := admit(ctx, conn)
	if err != nil {
		if peer.IsLeftAlready(err) {
			slog.Info("door answered a node that left", "err", err)
		} else {
			slog.Warn("door refused connection", "err", err)
		}
		conn.Close()
		return
	}
	a.handleConn(cctx, conn)
}

func (a *Angelus) handleConn(ctx context.Context, conn io.ReadWriteCloser) {
	if a.Handlers == nil {
		conn.Close()
		return
	}
	f := &follower{hubs: map[*ariaHub]struct{}{}}
	f.srv = jkrpc.NewServer(jkrpc.NewConn(conn), a.Handlers)
	defer f.close()

	done := make(chan struct{})
	go func() {
		f.srv.Serve(context.WithValue(ctx, followerKey{}, f))
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		f.srv.Stop()
	}
}
