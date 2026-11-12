package angelus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jack-work/figaro/internal/authz"
)

type WebAdmit func(ctx context.Context, r *http.Request) (context.Context, error)

type Web struct {
	Admit        WebAdmit
	MaxConnAge   time.Duration
	FollowMaxAge time.Duration
}

func (w Web) lapse() (time.Duration, string) {
	if w.FollowMaxAge > 0 && (w.MaxConnAge <= 0 || w.FollowMaxAge < w.MaxConnAge) {
		return w.FollowMaxAge, webFollowAgeReason
	}
	return w.MaxConnAge, webAgeReason
}

func (w Web) lapseFor(ctx context.Context) (time.Duration, string) {
	after, reason := w.lapse()
	if exp, ok := authz.ExpiryOf(ctx); ok {
		if left := time.Until(exp); after <= 0 || left < after {
			return max(left, time.Millisecond), webTokenReason
		}
	}
	return after, reason
}

const (
	webReadLimit         = 64 << 20
	webWriteGrace        = 500 * time.Millisecond
	webReadHeaderTimeout = 10 * time.Second
	webAgeReason         = "connection age limit: reconnect"
	webFollowAgeReason   = "follow age limit: reconnect for current groups"
	webTokenReason       = "the bearer expired: reconnect with a fresh token"
)

func ListenUnix(path string, mode os.FileMode) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (a *Angelus) ServeWeb(ctx context.Context, ln net.Listener, w Web) error {
	srv := &http.Server{
		Handler:           a.webDoor(ctx, w),
		ReadHeaderTimeout: webReadHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (a *Angelus) webDoor(ctx context.Context, w Web) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.Admit == nil {
			http.Error(rw, "this listener has no authenticator", http.StatusForbidden)
			return
		}
		conn, done := context.WithCancel(ctx)
		defer done()
		cctx, err := w.Admit(conn, r)
		if err == nil {
			err = a.seat(cctx, authz.DoorWeb)
		}
		if unseated := (*unseated)(nil); errors.As(err, &unseated) {
			slog.Warn("door refused upgrade", "door", authz.DoorWeb.String(), "err", err)
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		if err != nil {
			slog.Warn("door refused upgrade", "door", authz.DoorWeb.String(), "err", err)
			rw.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(webReadLimit)
		if after, reason := w.lapseFor(cctx); after > 0 {
			var end context.CancelCauseFunc
			cctx = authz.WithAdmittedUntil(cctx, time.Now().Add(after))
			cctx, end = context.WithCancelCause(cctx)
			defer end(nil)
			cctx = withLapse(cctx, lapse{after: after, reason: reason, end: end})
		}
		wc, release := newWebConn(ctx, cctx, c)
		defer release()
		a.handleConn(cctx, authz.DoorWeb, wc)
	})
}

type webConn struct {
	io     context.Context
	life   context.Context
	writes context.Context
	c      *websocket.Conn
	rbuf   []byte

	wmu  sync.Mutex
	wbuf []byte

	closeOnce sync.Once
}

func newWebConn(io, life context.Context, c *websocket.Conn) (*webConn, func()) {
	writes, stopWrites := context.WithCancel(io)
	stopAfter := context.AfterFunc(life, func() { time.AfterFunc(webWriteGrace, stopWrites) })
	return &webConn{io: io, life: life, writes: writes, c: c}, func() {
		stopAfter()
		stopWrites()
	}
}

func (w *webConn) Read(p []byte) (int, error) {
	for len(w.rbuf) == 0 {
		typ, data, err := w.c.Read(w.io)
		if err != nil {
			return 0, err
		}
		if typ != websocket.MessageText {
			return 0, fmt.Errorf("door: a frame is a text message, got %v", typ)
		}
		w.rbuf = append(data, '\n')
	}
	n := copy(p, w.rbuf)
	w.rbuf = w.rbuf[n:]
	return n, nil
}

func (w *webConn) Write(p []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.wbuf = append(w.wbuf, p...)
	for {
		i := bytes.IndexByte(w.wbuf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if err := w.life.Err(); err != nil {
			return 0, err
		}
		if err := w.c.Write(w.writes, websocket.MessageText, w.wbuf[:i]); err != nil {
			return 0, err
		}
		w.wbuf = append(w.wbuf[:0], w.wbuf[i+1:]...)
	}
}

func (w *webConn) Close() error {
	var err error
	w.closeOnce.Do(func() {
		if cause := context.Cause(w.life); cause != nil && !errors.Is(cause, context.Canceled) {
			err = w.c.Close(websocket.StatusPolicyViolation, closeReason(cause))
			return
		}
		err = w.c.Close(websocket.StatusNormalClosure, "")
	})
	return err
}

func (w *webConn) closeWith(reason error) error {
	var err error
	w.closeOnce.Do(func() { err = w.c.Close(websocket.StatusPolicyViolation, closeReason(reason)) })
	return err
}

func closeReason(err error) string {
	const max = 123
	msg := err.Error()
	if len(msg) > max {
		msg = msg[:max]
	}
	return msg
}
