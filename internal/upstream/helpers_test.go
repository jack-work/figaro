package upstream

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// blackhole sits between a client and a server and, once frozen, swallows
// every byte on the connections it already carries, the way a vanished
// source address does. Connections dialled after the freeze flow normally.
type blackhole struct {
	ln     net.Listener
	target string

	mu      sync.Mutex
	pipes   []*atomic.Bool
	clients []net.Conn
	dialed  atomic.Int32
}

func newBlackhole(t *testing.T, target string) *blackhole {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &blackhole{ln: ln, target: target}
	t.Cleanup(func() { ln.Close() })
	go b.serve()
	return b
}

func (b *blackhole) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		s, err := net.Dial("tcp", b.target)
		if err != nil {
			c.Close()
			continue
		}
		frozen := new(atomic.Bool)
		b.mu.Lock()
		b.pipes = append(b.pipes, frozen)
		b.clients = append(b.clients, c)
		b.mu.Unlock()
		go pump(c, s, frozen)
		go pump(s, c, frozen)
	}
}

// pump never closes a frozen side: a blackhole sends no FIN and no RST.
func pump(dst, src net.Conn, frozen *atomic.Bool) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if !frozen.Load() {
				dst.Close()
			}
			return
		}
	}
}

func (b *blackhole) freeze() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.pipes {
		p.Store(true)
	}
}

func (b *blackhole) cut() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.clients {
		c.Close()
	}
}

func (b *blackhole) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	b.dialed.Add(1)
	var d net.Dialer
	return d.DialContext(ctx, "tcp", b.ln.Addr().String())
}

func h2Server(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func serverTLS(srv *httptest.Server) *tls.Config {
	return srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
}

func get(ctx context.Context, rt http.RoundTripper, url string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

type httptestServer struct {
	url, addr string
	tls       *tls.Config
}
