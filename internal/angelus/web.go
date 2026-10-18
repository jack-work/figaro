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
)

type WebAdmit func(ctx context.Context, r *http.Request) (context.Context, error)

type Web struct {
	Admit      WebAdmit
	MaxConnAge time.Duration
}

const (
	webReadLimit         = 64 << 20
	webReadHeaderTimeout = 10 * time.Second
	webAgeReason         = "connection age limit: reconnect"
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
		cctx, err := w.Admit(ctx, r)
		if err != nil {
			slog.Warn("door refused upgrade", "err", err)
			rw.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(webReadLimit)
		if w.MaxConnAge > 0 {
			var cancel context.CancelFunc
			cctx, cancel = context.WithTimeout(cctx, w.MaxConnAge)
			defer cancel()
		}
		a.handleConn(cctx, &webConn{io: ctx, life: cctx, c: c})
	})
}

type webConn struct {
	io   context.Context
	life context.Context
	c    *websocket.Conn
	rbuf []byte

	wmu  sync.Mutex
	wbuf []byte

	closeOnce sync.Once
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
		if err := w.c.Write(w.io, websocket.MessageText, w.wbuf[:i]); err != nil {
			return 0, err
		}
		w.wbuf = append(w.wbuf[:0], w.wbuf[i+1:]...)
	}
}

func (w *webConn) Close() error {
	var err error
	w.closeOnce.Do(func() {
		if errors.Is(w.life.Err(), context.DeadlineExceeded) {
			err = w.c.Close(websocket.StatusPolicyViolation, webAgeReason)
			return
		}
		err = w.c.Close(websocket.StatusNormalClosure, "")
	})
	return err
}
