package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func streamingServer(t *testing.T) *httptestServer {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Write([]byte("event: message_start\n\n"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-t.Context().Done():
			}
			return
		}
		w.Write([]byte("ok"))
	}))
	return &httptestServer{srv.URL, srv.Listener.Addr().String(), serverTLS(srv)}
}

func TestARouteChangeFailsTheStreamsOnAMovedConnectionAtOnce(t *testing.T) {
	srv := streamingServer(t)
	hole := newBlackhole(t, srv.addr)
	rt := New(Config{Dial: hole.dial, TLS: srv.tls})

	req, _ := http.NewRequest(http.MethodGet, srv.url+"/stream", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 64)
	if _, err := resp.Body.Read(first); err != nil {
		t.Fatal(err)
	}

	rt.pool.source = func(*net.TCPAddr) (net.IP, error) { return net.ParseIP("10.73.0.196"), nil }
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Body)
		read <- err
	}()
	start := time.Now()
	rt.pool.recheck()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("the stream on a moved connection ended cleanly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream on a moved connection kept waiting after the route changed")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the stream took %v to fail", d)
	}
	if n := rt.Stats().Dropped[ReasonNetwork]; n != 1 {
		t.Fatalf("network drops %d, want 1 (%v)", n, rt.Stats().Dropped)
	}

	rt.pool.source = kernelSource
	if _, body, err := get(context.Background(), rt, srv.url); err != nil || string(body) != "ok" {
		t.Fatalf("the request after a route change: %q, %v", body, err)
	}
	if n := hole.dialed.Load(); n != 2 {
		t.Fatalf("dialled %d connections, want 2", n)
	}
}

func TestARouteChangeThatLeavesTheSourceAloneKeepsTheConnection(t *testing.T) {
	srv := streamingServer(t)
	hole := newBlackhole(t, srv.addr)
	rt := New(Config{Dial: hole.dial, TLS: srv.tls})

	mustGet(t, rt, srv.url)
	rt.pool.recheck()
	mustGet(t, rt, srv.url)
	if n := hole.dialed.Load(); n != 1 {
		t.Fatalf("dialled %d connections, want 1: an unrelated route change dropped a good connection", n)
	}
	if len(rt.Stats().Dropped) != 0 {
		t.Fatalf("dropped %v", rt.Stats().Dropped)
	}
}

func TestAnUnreachablePeerCountsAsMoved(t *testing.T) {
	srv := streamingServer(t)
	rt := New(Config{TLS: srv.tls})
	mustGet(t, rt, srv.url)
	rt.pool.source = func(*net.TCPAddr) (net.IP, error) { return nil, errors.New("network is unreachable") }
	rt.pool.recheck()
	if n := rt.Stats().Dropped[ReasonNetwork]; n != 1 {
		t.Fatalf("network drops %d, want 1", n)
	}
}
