package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsTransientStatus(t *testing.T) {
	transient := []int{429, 500, 502, 503, 504, 529}
	for _, c := range transient {
		if !isTransientStatus(c) {
			t.Errorf("status %d should be transient", c)
		}
	}
	for _, c := range []int{200, 400, 401, 403, 404, 422} {
		if isTransientStatus(c) {
			t.Errorf("status %d should NOT be transient", c)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	h := http.Header{}
	if got := parseRetryAfter(h); got != 0 {
		t.Errorf("absent: got %v want 0", got)
	}
	h.Set("Retry-After", "7")
	if got := parseRetryAfter(h); got != 7*time.Second {
		t.Errorf("numeric: got %v want 7s", got)
	}
	h.Set("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT") // HTTP-date not honored
	if got := parseRetryAfter(h); got != 0 {
		t.Errorf("http-date: got %v want 0", got)
	}
}

// staticAuth is a TokenResolver returning a fixed token; counts Invalidate.
type staticAuth struct {
	token       string
	invalidated int32
}

func (s *staticAuth) Resolve() (string, error) { return s.token, nil }
func (s *staticAuth) Invalidate(string) error  { atomic.AddInt32(&s.invalidated, 1); return nil }

// TestDoWithAuthRetry_RetriesTransient: a server that 529s twice then 200s must
// be retried through to success — an overload blip must not kill the turn.
func TestDoWithAuthRetry_RetriesTransient(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.WriteHeader(529) // overloaded
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	a := &Anthropic{auth: &staticAuth{token: "t"}, HTTPClient: srv.Client()}
	resp, _, err := a.doWithAuthRetry(context.Background(), func(string) (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server hits = %d, want 3 (2 transient + 1 success)", got)
	}
}

// TestDoWithAuthRetry_GivesUp: persistent 503 exhausts retries and errors,
// rather than hanging or succeeding.
func TestDoWithAuthRetry_GivesUp(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(503)
	}))
	defer srv.Close()

	a := &Anthropic{auth: &staticAuth{token: "t"}, HTTPClient: srv.Client()}
	_, _, err := a.doWithAuthRetry(context.Background(), func(string) (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&hits); got != maxTransientRetries+1 {
		t.Fatalf("server hits = %d, want %d", got, maxTransientRetries+1)
	}
}
