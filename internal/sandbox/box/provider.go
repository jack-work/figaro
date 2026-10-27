package box

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/jkrpc"
)

const (
	dialTimeout = 5 * time.Second
	seenCap     = 256 * 1024
)

var lostResult = sandbox.Result{ExitCode: -1}

type Provider struct {
	Name   string
	Socket string
	Scope  string
}

func (p Provider) Open(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	b := &Box{prov: p, spec: spec}
	l, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	b.cur = l
	return b, nil
}

func (p Provider) KillScope(ctx context.Context) (int, error) {
	b := &Box{prov: p}
	l, err := b.dial(ctx)
	if err != nil {
		return 0, err
	}
	defer l.rpc.Close()
	var n int
	err = l.rpc.Call(ctx, mReap, struct{}{}, &n)
	return n, fromWire(err)
}

type Box struct {
	prov Provider
	spec sandbox.Spec
	seq  atomic.Uint64
	mu   sync.Mutex
	cur  *link
	ws   string
	iso  sandbox.Isolation
	shut bool
}

type link struct {
	rpc   *jkrpc.Client
	mu    sync.Mutex
	procs map[string]*proc
	lost  bool
}

func (b *Box) dial(ctx context.Context) (*link, error) {
	d := net.Dialer{Timeout: dialTimeout}
	nc, err := d.DialContext(ctx, "unix", b.prov.Socket)
	if err != nil {
		return nil, fmt.Errorf("box %s: %w", b.prov.Name, err)
	}
	l := &link{procs: map[string]*proc{}}
	l.rpc = jkrpc.NewClient(jkrpc.NewConn(nc), l.notify)
	go func() {
		<-l.rpc.Done()
		l.drop(b.prov)
	}()
	var resp openResp
	if err := l.rpc.Call(ctx, mOpen, openReq{Scope: b.prov.Scope, Workspace: b.spec.Workspace, Env: b.spec.Env}, &resp); err != nil {
		l.rpc.Close()
		return nil, fmt.Errorf("box %s: open: %w", b.prov.Name, fromWire(err))
	}
	resp.Isolation.Provider = "box"
	b.mu.Lock()
	b.ws, b.iso = resp.Workspace, resp.Isolation
	b.mu.Unlock()
	return l, nil
}

func (l *link) notify(method string, params json.RawMessage) {
	switch method {
	case nChunk:
		var n chunkNote
		if json.Unmarshal(params, &n) == nil {
			if p := l.proc(n.Handle); p != nil {
				p.push(n.Data, nil)
			}
		}
	case nExit:
		var n exitNote
		if json.Unmarshal(params, &n) == nil {
			if p := l.proc(n.Handle); p != nil {
				l.forget(n.Handle)
				p.push(nil, &n.Result)
			}
		}
	}
}

func (l *link) proc(h string) *proc {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.procs[h]
}

func (l *link) track(h string, p *proc) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lost {
		return false
	}
	l.procs[h] = p
	return true
}

func (l *link) forget(h string) {
	l.mu.Lock()
	delete(l.procs, h)
	l.mu.Unlock()
}

func (l *link) drop(p Provider) {
	l.mu.Lock()
	l.lost = true
	procs := l.procs
	l.procs = nil
	l.mu.Unlock()
	note := []byte(fmt.Sprintf("\nbox %s: lost the connection to %s\n", p.Name, p.Socket))
	for _, pr := range procs {
		pr.push(note, &lostResult)
	}
}

func (b *Box) link(ctx context.Context) (*link, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shut {
		return nil, fmt.Errorf("box %s: closed", b.prov.Name)
	}
	select {
	case <-b.cur.rpc.Done():
	default:
		return b.cur, nil
	}
	b.mu.Unlock()
	l, err := b.dial(ctx)
	b.mu.Lock()
	if err != nil {
		return nil, err
	}
	if b.shut {
		l.rpc.Close()
		return nil, fmt.Errorf("box %s: closed", b.prov.Name)
	}
	select {
	case <-b.cur.rpc.Done():
		b.cur = l
	default:
		l.rpc.Close()
	}
	return b.cur, nil
}

func (b *Box) call(ctx context.Context, method string, params, result any) error {
	l, err := b.link(ctx)
	if err != nil {
		return err
	}
	err = l.rpc.Call(ctx, method, params, result)
	if undelivered(err) {
		l.rpc.Close()
		if l, err = b.link(ctx); err != nil {
			return err
		}
		err = l.rpc.Call(ctx, method, params, result)
	}
	return fromWire(err)
}

func undelivered(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "send: ") || msg == "connection closed"
}

func (b *Box) handle() string { return "h" + strconv.FormatUint(b.seq.Add(1), 10) }

func (b *Box) launch(ctx context.Context, method string, req sandbox.ExecRequest, sessionID string) (*proc, procResp, error) {
	l, err := b.link(ctx)
	if err != nil {
		return nil, procResp{}, err
	}
	h := b.handle()
	p := newProc(b, h, req.OnChunk)
	if !l.track(h, p) {
		return nil, procResp{}, fmt.Errorf("box %s: connection lost", b.prov.Name)
	}
	var resp procResp
	var params any = wireExec(h, req)
	if sessionID != "" {
		params = procReq{Handle: h, Session: sessionID}
	}
	err = l.rpc.Call(ctx, method, params, &resp)
	if undelivered(err) {
		l.forget(h)
		l.rpc.Close()
		if l, err = b.link(ctx); err != nil {
			p.push(nil, &lostResult)
			return nil, procResp{}, err
		}
		if !l.track(h, p) {
			p.push(nil, &lostResult)
			return nil, procResp{}, fmt.Errorf("box %s: connection lost", b.prov.Name)
		}
		err = l.rpc.Call(ctx, method, params, &resp)
	}
	if err != nil {
		l.forget(h)
		p.push(nil, &lostResult)
		return nil, procResp{}, fromWire(err)
	}
	p.pty, p.session = resp.PTY, resp.Session
	return p, resp, nil
}

func (b *Box) Exec(ctx context.Context, req sandbox.ExecRequest) (sandbox.Process, error) {
	if req.Command == "" {
		return nil, errors.New("command is required")
	}
	p, _, err := b.launch(ctx, mExec, req, "")
	if err != nil {
		return nil, err
	}
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				b.call(context.Background(), mSignal, procReq{Handle: p.handle, Signal: sigClose}, nil)
			case <-p.done:
			}
		}()
	}
	return p, nil
}

func (b *Box) Sessions() sandbox.Sessions { return sessions{b} }
func (b *Box) FS() sandbox.FS             { return boxFS{b} }

func (b *Box) Isolation() sandbox.Isolation {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.iso
}

func (b *Box) Workspace() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ws
}

func (b *Box) Close() error {
	b.mu.Lock()
	b.shut = true
	l := b.cur
	b.mu.Unlock()
	return l.rpc.Close()
}

type proc struct {
	box     *Box
	handle  string
	session string
	pty     bool
	onChunk func([]byte)

	mu     sync.Mutex
	seen   []byte
	queue  [][]byte
	exit   *sandbox.Result
	wake   chan struct{}
	done   chan struct{}
	res    sandbox.Result
	closed bool
}

func newProc(b *Box, h string, onChunk func([]byte)) *proc {
	p := &proc{box: b, handle: h, onChunk: onChunk, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go p.pump()
	return p
}

func (p *proc) push(chunk []byte, exit *sandbox.Result) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if chunk != nil {
		p.queue = append(p.queue, chunk)
		p.seen = append(p.seen, chunk...)
		if len(p.seen) > seenCap {
			p.seen = append([]byte(nil), p.seen[len(p.seen)-seenCap:]...)
		}
	}
	if exit != nil {
		p.exit, p.closed = exit, true
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *proc) pump() {
	for range p.wake {
		p.mu.Lock()
		q, exit := p.queue, p.exit
		p.queue = nil
		p.mu.Unlock()
		for _, c := range q {
			if p.onChunk != nil {
				p.onChunk(c)
			}
		}
		if exit != nil {
			p.mu.Lock()
			rest := p.queue
			p.queue = nil
			p.mu.Unlock()
			for _, c := range rest {
				if p.onChunk != nil {
					p.onChunk(c)
				}
			}
			p.res = *exit
			close(p.done)
			return
		}
	}
}

func (p *proc) target() procReq {
	if p.session != "" {
		return procReq{Session: p.session}
	}
	return procReq{Handle: p.handle}
}

func (p *proc) signal(sig string) error {
	r := p.target()
	r.Signal = sig
	return p.box.call(context.Background(), mSignal, r, nil)
}

func (p *proc) Wait() sandbox.Result  { <-p.done; return p.res }
func (p *proc) Done() <-chan struct{} { return p.done }
func (p *proc) PTY() bool             { return p.pty }
func (p *proc) Interrupt() error      { return p.signal(sigInt) }

func (p *proc) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	return p.signal(sigKill)
}

func (p *proc) Write(data []byte) (int, error) {
	r := p.target()
	r.Data = data
	var n int
	if err := p.box.call(context.Background(), mStdin, r, &n); err != nil {
		return 0, err
	}
	return n, nil
}

type session struct {
	*proc
	info sandbox.SessionInfo
}

func (s *session) ID() string { return s.session }

func (s *session) Info() sandbox.SessionInfo {
	var info sandbox.SessionInfo
	if err := s.box.call(context.Background(), mInfo, procReq{Session: s.session}, &info); err != nil {
		info = s.info
		select {
		case <-s.done:
			info.ExitCode, info.State = s.res.ExitCode, sandbox.SessionExited
			switch {
			case s.lost():
				info.State = sandbox.SessionKilled
			case s.res.TimedOut:
				info.State = sandbox.SessionTimedOut
			case s.res.Canceled:
				info.State = sandbox.SessionKilled
			}
		default:
		}
	}
	return info
}

func (s *session) lost() bool {
	select {
	case <-s.done:
		return s.res == lostResult
	default:
		return false
	}
}

func (s *session) Poll() []byte {
	var out dataResp
	s.box.call(context.Background(), mPoll, procReq{Session: s.session}, &out)
	return out.Data
}

func (s *session) Log() []byte {
	var out dataResp
	if err := s.box.call(context.Background(), mLog, procReq{Session: s.session}, &out); err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return append([]byte(nil), s.seen...)
	}
	return out.Data
}

type sessions struct{ b *Box }

func (v sessions) Start(req sandbox.ExecRequest) (sandbox.Session, error) {
	if req.Command == "" {
		return nil, errors.New("command is required")
	}
	p, resp, err := v.b.launch(context.Background(), mStart, req, "")
	if err != nil {
		return nil, err
	}
	return &session{proc: p, info: resp.Info}, nil
}

func (v sessions) Get(id string) (sandbox.Session, error) {
	p, resp, err := v.b.launch(context.Background(), mGet, sandbox.ExecRequest{}, id)
	if err != nil {
		return nil, err
	}
	return &session{proc: p, info: resp.Info}, nil
}

func (v sessions) List() []sandbox.SessionInfo {
	var out []sandbox.SessionInfo
	v.b.call(context.Background(), mList, struct{}{}, &out)
	return out
}

func (v sessions) Remove(id string) error {
	return v.b.call(context.Background(), mRemove, procReq{Session: id}, nil)
}

type boxFS struct{ b *Box }

const swapTries = 16

func (f boxFS) do(op, path string, req pathReq, out any) error {
	req.Path = path
	return pathErr(op, path, f.b.call(context.Background(), "box.fs."+op, req, out))
}

func (f boxFS) ReadFile(path string) ([]byte, error) {
	var out dataResp
	if err := f.do("read", path, pathReq{}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (f boxFS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return f.do("write", path, pathReq{Data: data, Perm: perm}, nil)
}

func (f boxFS) Stat(path string) (fs.FileInfo, error) {
	var out statResp
	if err := f.do("stat", path, pathReq{}, &out); err != nil {
		return nil, err
	}
	return fileInfo{out}, nil
}

func (f boxFS) MkdirAll(path string, perm fs.FileMode) error {
	return f.do("mkdirall", path, pathReq{Perm: perm}, nil)
}

func (f boxFS) Rename(oldpath, newpath string) error {
	return f.do("rename", oldpath, pathReq{To: newpath}, nil)
}

func (f boxFS) Update(path string, fn func(old []byte) ([]byte, error)) error {
	for range swapTries {
		old, err := f.ReadFile(path)
		if err != nil {
			return err
		}
		data, err := fn(old)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(old)
		err = f.do("swap", path, pathReq{Data: data, Old: sum[:]}, nil)
		if !errors.Is(err, errConflict) {
			return err
		}
	}
	return &fs.PathError{Op: "update", Path: path, Err: errConflict}
}
