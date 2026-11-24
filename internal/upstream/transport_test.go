package upstream

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestADeadHTTP2ConnectionFailsAndTheNextRequestDialsFresh(t *testing.T) {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	hole := newBlackhole(t, srv.Listener.Addr().String())
	rt := New(Config{
		SendPingTimeout: 300 * time.Millisecond,
		PingTimeout:     time.Second,
		Dial:            hole.dial,
		TLS:             serverTLS(srv),
	})

	resp, _, err := get(context.Background(), rt, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 2 {
		t.Fatalf("want HTTP/2 so requests share one connection, got %s", resp.Proto)
	}

	hole.freeze()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err = get(ctx, rt, srv.URL)
	if err == nil {
		t.Fatal("a request over a blackholed connection succeeded")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the request hung on the dead connection until its deadline (%v): no health check closed it", time.Since(start))
	}

	_, body, err := get(context.Background(), rt, srv.URL)
	if err != nil {
		t.Fatalf("the request after the dead connection was dropped: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if n := hole.dialed.Load(); n < 2 {
		t.Fatalf("dialled %d connections: the request after the drop did not dial a fresh one", n)
	}
}

func TestSharedCarriesTheHealthCheckAndTheStallWindow(t *testing.T) {
	tr := Shared()
	if h := tr.base.HTTP2; h == nil || h.SendPingTimeout != Defaults.SendPingTimeout || h.PingTimeout != Defaults.PingTimeout {
		t.Fatalf("shared transport has no HTTP/2 health check: %+v", h)
	}
	if tr.stall != Defaults.Stall || tr.stall <= 0 {
		t.Fatalf("shared transport stall window %v, want %v", tr.stall, Defaults.Stall)
	}
}
