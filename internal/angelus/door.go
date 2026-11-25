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
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/jkauthz"
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

func (f *follower) follow(hb *ariaHub, ch channels) bool { return f.join(hb, ch, true) }

func (f *follower) followUnlessFollowing(hb *ariaHub) bool { return f.join(hb, allChannels, false) }

func (f *follower) join(hb *ariaHub, ch channels, replace bool) (joined bool) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return false
	}
	_, already := f.hubs[hb]
	if already && !replace {
		f.mu.Unlock()
		return false
	}
	f.hubs[hb] = struct{}{}
	f.mu.Unlock()
	if !hb.add(f, ch) {
		f.forget(hb)
		return false
	}
	return !already
}

func (f *follower) unfollow(id string) bool {
	f.mu.Lock()
	var held []*ariaHub
	for hb := range f.hubs {
		if hb.id == id {
			held = append(held, hb)
			delete(f.hubs, hb)
		}
	}
	f.mu.Unlock()
	for _, hb := range held {
		hb.remove(f)
	}
	return len(held) > 0
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

func (f *follower) present(ctx context.Context, token string) {
	f.mu.Lock()
	links := make([]*peer.Link, 0, len(f.links))
	for _, l := range f.links {
		links = append(links, l)
	}
	f.mu.Unlock()
	params, err := json.Marshal(rpc.PresentRequest{Token: token})
	if err != nil {
		return
	}
	for _, l := range links {
		if _, err := l.Call(ctx, rpc.MethodEndpointPresent, params); err != nil {
			slog.Warn("endpoint.present on a forwarded link failed; it ends at its old expiry", "node", l.Node(), "err", err)
		}
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

func (a *Angelus) Serve(ctx context.Context, ln net.Listener, door authz.Door, admit Admit) error {
	if admit == nil {
		admit = authz.Admit
	}
	leave, ok := a.enterDoor("door "+door.String(), ln)
	if !ok {
		return errStopping
	}
	defer leave()
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
		go a.admitConn(ctx, conn, door, admit)
	}
}

func (a *Angelus) admitConn(ctx context.Context, conn net.Conn, door authz.Door, admit Admit) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cctx, err := admit(ctx, conn)
	if err == nil {
		err = a.seat(cctx, door)
	}
	if err != nil {
		if peer.IsLeftAlready(err) {
			slog.Info("door answered a node that left", "err", err)
		} else {
			slog.Warn("door refused connection", "door", door.String(), "err", err)
		}
		conn.Close()
		return
	}
	a.handleConn(cctx, door, conn)
}

type unseated struct {
	door    authz.Door
	subject jkauthz.Principal
}

func (e *unseated) Error() string {
	return fmt.Sprintf("the %s door seats only a subject the policy binds to a role, and %s holds none", e.door, e.subject)
}

func (a *Angelus) seat(ctx context.Context, door authz.Door) error {
	if !door.SeatsOnlyPolicySubjects() {
		return nil
	}
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return err
	}
	if !a.holdsRole(sub) {
		return &unseated{door: door, subject: sub.Principal}
	}
	return nil
}

func (a *Angelus) holdsRole(sub jkauthz.Subject) bool {
	if a.Policies == nil {
		return authz.IsOwner(a.owner(), sub)
	}
	return a.Policies.HoldsRole(sub)
}

func (a *Angelus) handleConn(ctx context.Context, door authz.Door, conn io.ReadWriteCloser) {
	ctx = authz.WithDoor(ctx, door)
	if a.Handlers == nil {
		conn.Close()
		return
	}
	life := lifeOf(ctx)
	if life == nil {
		life = newLifetime(0, "", nil)
		ctx = context.WithValue(ctx, lifeKey{}, life)
	}
	life.settle(ctx)
	out := newOutbox(conn, a.outboxBudget(), func(err error) {
		slog.Warn("door dropped a follower", "door", door.String(), "err", err)
		endWith(conn, err)
	})
	defer out.Close()
	f := &follower{hubs: map[*ariaHub]struct{}{}}
	rc := jkrpc.NewConn(capFrames(out, maxDoorFrame))
	f.srv = jkrpc.NewServer(rc, a.Handlers)
	defer f.close()

	done := make(chan struct{})
	go func() {
		if err := serveRequests(context.WithValue(ctx, followerKey{}, f), rc, a.Handlers); errors.Is(err, errFrameTooLarge) {
			slog.Warn("door closed a connection", "door", door.String(), "err", err)
		}
		close(done)
	}()
	for {
		due, why, moved, end := life.next()
		var lapsed <-chan time.Time
		var timer *time.Timer
		if !due.IsZero() {
			timer = time.NewTimer(time.Until(due))
			lapsed = timer.C
		}
		select {
		case <-done:
		case <-moved:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-lapsed:
			_ = f.srv.Notify(rpc.MethodDoorLapsed, rpc.DoorLapsed{Reason: why, AfterMS: time.Since(life.born).Milliseconds()})
			out.drain(lapseNoticeGrace)
			if end != nil {
				end(errors.New(why))
			}
			f.srv.Stop()
		case <-ctx.Done():
			if shut := doorShut(ctx); shut != nil {
				_ = f.srv.Notify(rpc.MethodDoorLapsed, rpc.DoorLapsed{Reason: shut.Reason})
				out.drain(shut.noticeGrace())
			}
			f.srv.Stop()
		}
		if timer != nil {
			timer.Stop()
		}
		return
	}
}

const (
	lapseNoticeGrace = 2 * time.Second
	stopNoticeGrace  = 250 * time.Millisecond
	stoppingReason   = "daemon stopping"
)

type reasonedCloser interface {
	closeWith(reason error) error
}

func endWith(conn io.Closer, reason error) {
	if r, ok := conn.(reasonedCloser); ok {
		r.closeWith(reason)
		return
	}
	conn.Close()
}

type lifetime struct {
	born   time.Time
	maxAge time.Duration
	ageWhy string
	end    context.CancelCauseFunc

	mu    sync.Mutex
	due   time.Time
	why   string
	moved chan struct{}
}

type lifeKey struct{}

func newLifetime(maxAge time.Duration, ageWhy string, end context.CancelCauseFunc) *lifetime {
	return &lifetime{born: time.Now(), maxAge: maxAge, ageWhy: ageWhy, end: end, moved: make(chan struct{})}
}

func lifeOf(ctx context.Context) *lifetime {
	l, _ := ctx.Value(lifeKey{}).(*lifetime)
	return l
}

func (l *lifetime) settle(ctx context.Context) {
	var due time.Time
	var why string
	if l.maxAge > 0 {
		due, why = l.born.Add(l.maxAge), l.ageWhy
	}
	if exp, ok := authz.ExpiryOf(ctx); ok && (due.IsZero() || exp.Before(due)) {
		due, why = exp, webTokenReason
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if due.Equal(l.due) && why == l.why {
		return
	}
	l.due, l.why = due, why
	close(l.moved)
	l.moved = make(chan struct{})
}

func (l *lifetime) next() (time.Time, string, <-chan struct{}, context.CancelCauseFunc) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.due, l.why, l.moved, l.end
}
