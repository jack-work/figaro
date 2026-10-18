package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

type follower struct {
	srv *jkrpc.Server

	mu     sync.Mutex
	hubs   map[*ariaHub]struct{}
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
	f.hubs = nil
	f.mu.Unlock()
	for _, hb := range hubs {
		hb.remove(f)
	}
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
	cctx, err := admit(ctx, conn)
	if err != nil {
		slog.Warn("door refused connection", "err", err)
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
