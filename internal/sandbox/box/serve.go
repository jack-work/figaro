package box

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/jkrpc"
)

func Listen(path string) (net.Listener, error) {
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("box: %s is already served", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", path)
}

type Server struct {
	root   string
	host   *local.Host
	mu     sync.Mutex
	scopes map[string]bool
}

func NewServer(workspace string) (*Server, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("box: workspace %q is not a directory", workspace)
	}
	return &Server{root: root, host: local.NewHost(local.DefaultTTL), scopes: map[string]bool{}}, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		s.mu.Lock()
		for scope := range s.scopes {
			s.host.KillScope(scope)
		}
		s.mu.Unlock()
	}()
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveConn(ctx, nc)
		}()
	}
}

type running struct {
	proc   sandbox.Process
	cancel context.CancelFunc
}

type serverConn struct {
	srv    *Server
	rpc    *jkrpc.Server
	ctx    context.Context
	mu     sync.Mutex
	box    sandbox.Sandbox
	ws     string
	procs  map[string]running
	cancel context.CancelFunc
}

func (s *Server) serveConn(parent context.Context, nc net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	c := &serverConn{srv: s, ctx: ctx, cancel: cancel, procs: map[string]running{}}
	c.rpc = jkrpc.NewServer(jkrpc.NewConn(nc), c.handlers())
	stop := context.AfterFunc(ctx, c.rpc.Stop)
	c.rpc.Serve(ctx)
	stop()
	cancel()
	nc.Close()
	c.mu.Lock()
	procs := c.procs
	c.procs = map[string]running{}
	c.mu.Unlock()
	for _, r := range procs {
		<-r.proc.Done()
	}
}

func handle[T any](fn func(T) (any, error)) jkrpc.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var req T
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, &jkrpc.Error{Code: -32602, Message: err.Error()}
			}
		}
		out, err := fn(req)
		return out, toWire(err)
	}
}

func (c *serverConn) handlers() map[string]jkrpc.HandlerFunc {
	return map[string]jkrpc.HandlerFunc{
		mOpen:   handle(c.open),
		mExec:   handle(c.exec),
		mSignal: handle(c.signal),
		mStdin:  handle(c.stdin),
		mStart:  handle(c.start),
		mGet:    handle(c.get),
		mList: handle(func(struct{}) (any, error) {
			b, err := c.opened()
			if err != nil {
				return nil, err
			}
			return b.Sessions().List(), nil
		}),
		mRemove: handle(c.sessionDo(func(b sandbox.Sandbox, id string) (any, error) { return nil, b.Sessions().Remove(id) })),
		mPoll:   handle(c.sessionOp(func(s sandbox.Session) any { return dataResp{Data: s.Poll()} })),
		mLog:    handle(c.sessionOp(func(s sandbox.Session) any { return dataResp{Data: s.Log()} })),
		mInfo:   handle(c.sessionOp(func(s sandbox.Session) any { return s.Info() })),
		mRead: handle(c.fsOp(func(f sandbox.FS, r pathReq) (any, error) {
			data, err := f.ReadFile(r.Path)
			return dataResp{Data: data}, err
		})),
		mWrite: handle(c.fsOp(func(f sandbox.FS, r pathReq) (any, error) { return nil, f.WriteFile(r.Path, r.Data, r.Perm) })),
		mStat: handle(c.fsOp(func(f sandbox.FS, r pathReq) (any, error) {
			fi, err := f.Stat(r.Path)
			if err != nil {
				return nil, err
			}
			return wireStat(fi), nil
		})),
		mMkdir:  handle(c.fsOp(func(f sandbox.FS, r pathReq) (any, error) { return nil, f.MkdirAll(r.Path, r.Perm) })),
		mRename: handle(c.fsOp(func(f sandbox.FS, r pathReq) (any, error) { return nil, f.Rename(r.Path, r.To) })),
		mSwap: handle(c.fsOp(func(f sandbox.FS, r pathReq) (any, error) {
			return nil, f.Update(r.Path, func(old []byte) ([]byte, error) {
				if sum := sha256.Sum256(old); !bytes.Equal(sum[:], r.Old) {
					return nil, errConflict
				}
				return r.Data, nil
			})
		})),
	}
}

func (c *serverConn) open(r openReq) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.box != nil {
		return nil, errors.New("box: this connection is already open")
	}
	ws := c.srv.root
	if p := r.Workspace; p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(ws, p)
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			ws = p
		}
	}
	b, err := local.Provider{Host: c.srv.host, Scope: r.Scope}.Open(c.ctx, sandbox.Spec{Workspace: ws, Env: r.Env})
	if err != nil {
		return nil, err
	}
	c.srv.mu.Lock()
	c.srv.scopes[r.Scope] = true
	c.srv.mu.Unlock()
	c.box, c.ws = b, ws
	return openResp{Workspace: ws, Isolation: b.Isolation()}, nil
}

func (c *serverConn) opened() (sandbox.Sandbox, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.box == nil {
		return nil, errors.New("box: call box.open first")
	}
	return c.box, nil
}

func (c *serverConn) chunks(h string) func([]byte) {
	return func(b []byte) { c.rpc.Notify(nChunk, chunkNote{Handle: h, Data: b}) }
}

func (c *serverConn) exec(r execReq) (any, error) {
	b, err := c.opened()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(c.ctx)
	req := r.request()
	req.OnChunk = c.chunks(r.Handle)
	p, err := b.Exec(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	c.mu.Lock()
	c.procs[r.Handle] = running{proc: p, cancel: cancel}
	c.mu.Unlock()
	go func() {
		res := p.Wait()
		c.mu.Lock()
		delete(c.procs, r.Handle)
		c.mu.Unlock()
		cancel()
		c.rpc.Notify(nExit, exitNote{Handle: r.Handle, Result: res})
	}()
	return procResp{PTY: p.PTY()}, nil
}

func (c *serverConn) watch(h string, s sandbox.Session) {
	go func() {
		select {
		case <-s.Done():
			c.rpc.Notify(nExit, exitNote{Handle: h, Result: s.Wait()})
		case <-c.ctx.Done():
		}
	}()
}

func (c *serverConn) start(r execReq) (any, error) {
	b, err := c.opened()
	if err != nil {
		return nil, err
	}
	req := r.request()
	ctx := c.ctx
	notify := c.chunks(r.Handle)
	req.OnChunk = func(p []byte) {
		if ctx.Err() == nil {
			notify(p)
		}
	}
	s, err := b.Sessions().Start(req)
	if err != nil {
		return nil, err
	}
	c.watch(r.Handle, s)
	return procResp{Session: s.ID(), PTY: s.PTY(), Info: s.Info()}, nil
}

func (c *serverConn) get(r procReq) (any, error) {
	b, err := c.opened()
	if err != nil {
		return nil, err
	}
	s, err := b.Sessions().Get(r.Session)
	if err != nil {
		return nil, err
	}
	c.watch(r.Handle, s)
	return procResp{Session: s.ID(), PTY: s.PTY(), Info: s.Info()}, nil
}

func (c *serverConn) process(r procReq) (sandbox.Process, context.CancelFunc, error) {
	if r.Session != "" {
		b, err := c.opened()
		if err != nil {
			return nil, nil, err
		}
		s, err := b.Sessions().Get(r.Session)
		if err != nil {
			return nil, nil, err
		}
		return s, func() { s.Kill() }, nil
	}
	c.mu.Lock()
	run, ok := c.procs[r.Handle]
	c.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("box: no running process %q", r.Handle)
	}
	return run.proc, run.cancel, nil
}

func (c *serverConn) signal(r procReq) (any, error) {
	p, cancel, err := c.process(r)
	if err != nil {
		return nil, err
	}
	switch r.Signal {
	case sigKill:
		return nil, p.Kill()
	case sigInt:
		return nil, p.Interrupt()
	case sigClose:
		cancel()
		return nil, nil
	}
	return nil, fmt.Errorf("box: unknown signal %q", r.Signal)
}

func (c *serverConn) stdin(r procReq) (any, error) {
	p, _, err := c.process(r)
	if err != nil {
		return nil, err
	}
	n, err := p.Write(r.Data)
	return n, err
}

func (c *serverConn) sessionDo(fn func(sandbox.Sandbox, string) (any, error)) func(procReq) (any, error) {
	return func(r procReq) (any, error) {
		b, err := c.opened()
		if err != nil {
			return nil, err
		}
		return fn(b, r.Session)
	}
}

func (c *serverConn) sessionOp(fn func(sandbox.Session) any) func(procReq) (any, error) {
	return c.sessionDo(func(b sandbox.Sandbox, id string) (any, error) {
		s, err := b.Sessions().Get(id)
		if err != nil {
			return nil, err
		}
		return fn(s), nil
	})
}

func (c *serverConn) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return filepath.Join(c.ws, p)
}

func (c *serverConn) fsOp(fn func(sandbox.FS, pathReq) (any, error)) func(pathReq) (any, error) {
	return func(r pathReq) (any, error) {
		b, err := c.opened()
		if err != nil {
			return nil, err
		}
		r.Path, r.To = c.resolve(r.Path), c.resolve(r.To)
		return fn(b.FS(), r)
	}
}
