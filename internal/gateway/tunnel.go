// Package gateway serves an HTTP door onto figaro.
//
// Two faces on one listener. The TUNNEL carries the existing JSON-RPC
// surface unchanged: it is a byte pump between a WebSocket and the angelus
// unix socket, and it does not parse the protocol it carries. That is the
// whole reason it cannot drift from the contract, and why a method added to
// api/rpc needs no change here, ever.
//
// The FORM face (forms.go) is the other half: REST + SSE shaped for a
// browser, which cannot usefully speak the tunnel.
package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Dialer opens a connection to the daemon this gateway fronts. It is a
// function rather than an address so a test can hand back a pipe, and so the
// gateway never grows an opinion about where the daemon lives.
type Dialer func(ctx context.Context) (net.Conn, error)

// UnixDialer is the production Dialer: the angelus socket.
func UnixDialer(path string) Dialer {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
}

// Tunnel serves GET /v1/socket: upgrade, then copy bytes both ways.
type Tunnel struct {
	Dial Dialer
	// Origins that may open a tunnel from a browser. Empty means no browser
	// origin is accepted, which is the safe default: the CLI sends no Origin
	// header and is unaffected, while a page on any site is refused.
	Origins []string
	// Filter, when non-nil, inspects each client->server frame's method and
	// may refuse it. Nil is a pure pump. See filter.go.
	Filter *Filter
	// MaxAge bounds one connection's life. IT IS AN AUTHORIZATION CONTROL,
	// not a resource one: forward authentication happens on the UPGRADE
	// REQUEST ONLY, so once a socket is established no frame is ever
	// re-authorized and a revoked operator keeps a live shell for as long as
	// the connection lasts. Capping it forces a re-upgrade, and a re-upgrade
	// is re-authorized. Zero means no cap.
	MaxAge time.Duration
	// Keepalive is the server ping interval. Cloudflare reaps an idle
	// WebSocket at roughly 100s and it is not configurable on a tunnel, so
	// an idle connection MUST be kept warm or it dies the moment the
	// operator stops typing -- a failure that never shows up in a test and
	// always shows up in use. Zero disables, which is only safe locally.
	Keepalive time.Duration
	Log       *slog.Logger
}

func (t *Tunnel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: t.Origins,
		// The payload is NDJSON, which compresses well, but the frames are
		// small and latency matters more than bytes on a local network.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept has already written a response.
		return
	}
	// No read limit: an aria.Page carrying a large tool output is a
	// legitimate frame and the default 32KiB cap would sever the connection
	// mid-conversation. The daemon is the authority on frame size.
	ws.SetReadLimit(-1)

	// The connection's own lifetime, independent of the request context:
	// r.Context() is done when ServeHTTP returns, and we outlive that.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	if t.MaxAge > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, t.MaxAge)
		defer stop()
	}

	upstream, err := t.Dial(ctx)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "daemon unreachable")
		return
	}
	defer upstream.Close()

	// NetConn gives us the io.ReadWriteCloser jkrpc was written against, so
	// both ends of this tunnel are the same stream type the unix socket
	// always was. MessageBinary because the payload is a byte stream, not a
	// sequence of text messages: the framing that matters is the NDJSON
	// newline, and a WebSocket message boundary carries no meaning here.
	down := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer down.Close()

	if t.Keepalive > 0 {
		go t.ping(ctx, ws)
	}
	// Whatever ends the connection -- MaxAge, shutdown, a dead peer -- has
	// to reach the two io.Copy goroutines, which are blocked in Read and
	// will not notice a context on their own.
	go func() {
		<-ctx.Done()
		_ = down.Close()
		_ = upstream.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// client -> daemon. The only direction a filter can act on: a request
	// names a method, a response does not.
	go func() {
		defer wg.Done()
		var err error
		if t.Filter != nil {
			err = t.Filter.Pump(down, upstream)
		} else {
			_, err = io.Copy(upstream, down)
		}
		t.logCopyErr("client->daemon", err)
		// Half-close so the daemon sees EOF and drops its side rather than
		// waiting on a client that has gone.
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = upstream.Close()
		}
	}()

	// daemon -> client. ALWAYS a pure copy: nothing on the way out is
	// re-encoded, so no field can be lost in translation.
	go func() {
		defer wg.Done()
		_, err := io.Copy(down, upstream)
		t.logCopyErr("daemon->client", err)
		_ = down.Close()
	}()

	wg.Wait()
	ws.Close(websocket.StatusNormalClosure, "")
}

func (t *Tunnel) logCopyErr(dir string, err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled) {
		return
	}
	if t.Log != nil {
		t.Log.Debug("tunnel copy ended", "dir", dir, "err", err)
	}
}

// ping keeps the connection warm.
//
// Cloudflare reaps an idle WebSocket at roughly 100 seconds and the timeout
// is not configurable on a tunnel. An operator who stops typing for two
// minutes would otherwise find the socket gone -- and because the reaper
// only fires on IDLE connections, the failure is invisible during any test
// that keeps sending. It has to be shipped with the feature, not after it.
//
// websocket.Ping blocks for the pong, so a peer that has stopped answering
// ends the loop and the deferred close tears the tunnel down: the keepalive
// doubles as liveness detection.
func (t *Tunnel) ping(ctx context.Context, ws *websocket.Conn) {
	tick := time.NewTicker(t.Keepalive)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, t.Keepalive)
			err := ws.Ping(pctx)
			cancel()
			if err != nil {
				// Not an error worth shouting about: a closed peer is the
				// ordinary end of a tunnel. The copy loops will notice.
				return
			}
		}
	}
}
