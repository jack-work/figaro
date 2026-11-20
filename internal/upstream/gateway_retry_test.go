package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quick = Config{GatewayBackoff: time.Millisecond}

func post(ctx context.Context, rt http.RoundTripper, url, body string) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(WithGatewayRetry(ctx), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	return resp, string(got), err
}

func scripted(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32, *[]string) {
	t.Helper()
	var n atomic.Int32
	bodies := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, string(b))
		i := int(n.Add(1)) - 1
		status := statuses[min(i, len(statuses)-1)]
		w.WriteHeader(status)
		io.WriteString(w, "error code: "+http.StatusText(status))
	}))
	t.Cleanup(srv.Close)
	return srv, &n, bodies
}

func TestAGatewayRestartIsRetriedOnceAndTheTurnCompletes(t *testing.T) {
	for _, down := range []int{502, 503} {
		srv, n, bodies := scripted(t, down, 200)
		resp, _, err := post(context.Background(), New(quick), srv.URL, `{"prompt":"hi"}`)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || n.Load() != 2 {
			t.Fatalf("status %d after %d requests, want 200 after 2 (down=%d)", resp.StatusCode, n.Load(), down)
		}
		if (*bodies)[0] != `{"prompt":"hi"}` || (*bodies)[1] != (*bodies)[0] {
			t.Fatalf("the retry did not resend the body: %q", *bodies)
		}
	}
}

func TestTwoGatewayFailuresFailTheRequestWithTheSecondAnswer(t *testing.T) {
	srv, n, _ := scripted(t, 502, 502, 200)
	resp, body, err := post(context.Background(), New(quick), srv.URL, "x")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 502 || n.Load() != 2 {
		t.Fatalf("status %d after %d requests, want 502 after exactly 2", resp.StatusCode, n.Load())
	}
	if !strings.Contains(body, "error code") {
		t.Fatalf("the failing answer lost its body: %q", body)
	}
}

func TestOtherErrorStatusesAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 429, 500, 504, 529} {
		srv, n, _ := scripted(t, status, 200)
		resp, _, err := post(context.Background(), New(quick), srv.URL, "x")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status || n.Load() != 1 {
			t.Fatalf("%d: status %d after %d requests", status, resp.StatusCode, n.Load())
		}
	}
}

func TestAFailureAfterOutputBeganIsNotRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	resp, body, err := post(context.Background(), New(quick), srv.URL, "x")
	if resp == nil || resp.StatusCode != 200 {
		t.Fatalf("the stream did not start: %v", err)
	}
	if err == nil {
		t.Fatalf("the cut stream read clean: %q", body)
	}
	if n.Load() != 1 {
		t.Fatalf("%d requests: a stream that began was retried", n.Load())
	}
}

func TestABodyThatCannotBeReplayedIsNotRetried(t *testing.T) {
	srv, n, _ := scripted(t, 502, 200)
	req, _ := http.NewRequestWithContext(WithGatewayRetry(context.Background()), http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("x")))
	resp, err := New(quick).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 || n.Load() != 1 {
		t.Fatalf("status %d after %d requests, want the 502 after 1", resp.StatusCode, n.Load())
	}
}

func TestACancelDuringTheBackoffEndsTheRetry(t *testing.T) {
	srv, n, _ := scripted(t, 502, 200)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, _, err := post(ctx, New(Config{GatewayBackoff: time.Hour}), srv.URL, "x")
	if err == nil || ctx.Err() == nil {
		t.Fatalf("err %v: the cancelled retry did not end", err)
	}
	if n.Load() != 1 {
		t.Fatalf("%d requests", n.Load())
	}
}

func TestAProviderThatDidNotOptInIsNeverRetried(t *testing.T) {
	srv, n, _ := scripted(t, 502, 200)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("x"))
	resp, err := New(quick).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 || n.Load() != 1 {
		t.Fatalf("status %d after %d requests, want the 502 after 1", resp.StatusCode, n.Load())
	}
}

func TestAnAnswerFromAServedUpstreamIsNeverRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set(ServedHeader, "anthropic-kic")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	resp, _, err := post(context.Background(), New(quick), srv.URL, "x")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 502 || n.Load() != 1 {
		t.Fatalf("status %d after %d requests: a 502 the upstream served was replayed", resp.StatusCode, n.Load())
	}
}
