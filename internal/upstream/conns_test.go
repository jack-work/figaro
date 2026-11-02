package upstream

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnIdleConnectionPastTheAgeCapIsRetired(t *testing.T) {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	hole := newBlackhole(t, srv.Listener.Addr().String())
	rt := New(Config{MaxAge: 200 * time.Millisecond, Sweep: 20 * time.Millisecond, Dial: hole.dial, TLS: serverTLS(srv)})

	if _, _, err := get(context.Background(), rt, srv.URL); err != nil {
		t.Fatal(err)
	}
	if n := len(rt.pool.live()); n != 1 {
		t.Fatalf("%d live connections after one request, want 1", n)
	}
	waitFor(t, "the idle connection to pass the age cap and be retired", func() bool { return len(rt.pool.live()) == 0 })

	if _, _, err := get(context.Background(), rt, srv.URL); err != nil {
		t.Fatalf("the request after a retirement failed: %v", err)
	}
	if n := hole.dialed.Load(); n != 2 {
		t.Fatalf("dialled %d connections, want 2", n)
	}
}

func TestAConnectionCarryingAStreamIsNotRetiredForAge(t *testing.T) {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 12; i++ {
			w.Write([]byte("event: ping\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	hole := newBlackhole(t, srv.Listener.Addr().String())
	rt := New(Config{MaxAge: 100 * time.Millisecond, Sweep: 20 * time.Millisecond, Dial: hole.dial, TLS: serverTLS(srv)})

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("a stream running past the age cap was cut: %v (after %d bytes)", err, len(body))
	}
	if n := hole.dialed.Load(); n != 1 {
		t.Fatalf("dialled %d connections, want 1", n)
	}
	waitFor(t, "the connection to be retired once its stream ended", func() bool { return len(rt.pool.live()) == 0 })
}

func TestTheSweeperStopsWhenNoConnectionIsLeft(t *testing.T) {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	rt := New(Config{MaxAge: 50 * time.Millisecond, Sweep: 10 * time.Millisecond, TLS: serverTLS(srv)})
	if _, _, err := get(context.Background(), rt, srv.URL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sweeper to stop", func() bool {
		rt.pool.mu.Lock()
		defer rt.pool.mu.Unlock()
		return !rt.pool.sweeping
	})
}
