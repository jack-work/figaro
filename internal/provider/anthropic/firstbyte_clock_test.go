package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/provider"
)

// The first-byte clock times the upstream: from the request written to its
// first byte, never our own backoff or the body upload.

func scriptedGateway(t *testing.T, header func(n int32, h http.Header) int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		n := atomic.AddInt32(&hits, 1)
		w.WriteHeader(header(n, w.Header()))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func sendThroughTheClock(t *testing.T, srv *httptest.Server, window time.Duration, body func() io.Reader) error {
	t.Helper()
	a := &Anthropic{auth: &staticAuth{token: "t"}, HTTPClient: srv.Client()}
	bound, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx, settle := provider.WatchFirstByte(bound, window)
	resp, _, err := a.doWithAuthRetry(ctx, func(string) (*http.Request, error) {
		var b io.Reader
		if body != nil {
			b = body()
		}
		return http.NewRequestWithContext(ctx, "POST", srv.URL, b)
	})
	if err == nil {
		provider.SawByte(ctx)
		resp.Body.Close()
	}
	return settle(err)
}

func TestAShortRateLimitRetriesOffTheFirstByteClock(t *testing.T) {
	srv, hits := scriptedGateway(t, func(n int32, h http.Header) int {
		if n == 1 {
			h.Set("Retry-After", "1")
			return http.StatusTooManyRequests
		}
		return http.StatusOK
	})
	if err := sendThroughTheClock(t, srv, 300*time.Millisecond, nil); err != nil {
		t.Fatalf("a 1 s backoff under a 300 ms first-byte window failed the send: %v", err)
	}
	if n := atomic.LoadInt32(hits); n != 2 {
		t.Fatalf("the upstream saw %d sends, want 2", n)
	}
}

func TestTheFirstByteClockStartsWhenTheBodyIsWritten(t *testing.T) {
	srv, _ := scriptedGateway(t, func(int32, http.Header) int { return http.StatusOK })
	slowBody := func() io.Reader {
		r, w := io.Pipe()
		go func() {
			for range 4 {
				time.Sleep(100 * time.Millisecond)
				w.Write([]byte("chunk"))
			}
			w.Close()
		}()
		return r
	}
	if err := sendThroughTheClock(t, srv, 200*time.Millisecond, slowBody); err != nil {
		t.Fatalf("a 400 ms upload under a 200 ms first-byte window failed the send: %v", err)
	}
}

func TestAnUpstreamThatNeverAnswersStillTripsTheClock(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	err := sendThroughTheClock(t, srv, 200*time.Millisecond, nil)
	if _, cut := provider.AsStreamCut(err); !cut {
		t.Fatalf("a silent upstream gave %v, want a first-byte stream cut", err)
	}
}

func TestAnOverloadIsNeverSleptPastTheBound(t *testing.T) {
	old := retryAfterBound
	retryAfterBound = 200 * time.Millisecond
	defer func() { retryAfterBound = old }()
	srv, hits := scriptedGateway(t, func(n int32, h http.Header) int {
		if n == 1 {
			h.Set("Retry-After", "3600")
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	})
	start := time.Now()
	if err := sendThroughTheClock(t, srv, time.Second, nil); err != nil {
		t.Fatalf("a 503 asking for an hour failed the send: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second || atomic.LoadInt32(hits) != 2 {
		t.Fatalf("a 503 asking for an hour was slept for %s (%d sends)", took, atomic.LoadInt32(hits))
	}
}
