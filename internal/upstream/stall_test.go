package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func stallTransport(t *testing.T, h http.Handler, window time.Duration) (*Transport, string) {
	t.Helper()
	srv := h2Server(t, h)
	return New(Config{Stall: window, TLS: serverTLS(srv)}), srv.URL
}

func hold(t *testing.T, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-t.Context().Done():
	}
}

func TestAStreamThatGoesSilentFailsAsStalled(t *testing.T) {
	rt, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("event: message_start\n\n"))
		w.(http.Flusher).Flush()
		hold(t, r)
	}), 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("a silent stream ended with %v, want ErrStalled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("a stall reads as a user cancel: the turn would keep the partial output")
	}
	if ctx.Err() != nil {
		t.Fatal("the stall was only noticed at the caller's deadline")
	}
}

func TestNoResponseHeadersFailsAsStalled(t *testing.T) {
	rt, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold(t, r)
	}), 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := get(ctx, rt, url)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("got %v, want ErrStalled", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the stall was only noticed at the caller's deadline")
	}
}

func TestAStreamLongerThanTheWindowIsNotAStallWhileBytesFlow(t *testing.T) {
	rt, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 10; i++ {
			w.Write([]byte("event: ping\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
		w.Write([]byte("done"))
	}), 200*time.Millisecond)

	_, body, err := get(context.Background(), rt, url)
	if err != nil {
		t.Fatalf("a stream that kept sending for 3x the window failed: %v", err)
	}
	if !strings.HasSuffix(string(body), "done") {
		t.Fatalf("body %q", body)
	}
}

type tricklingBody struct {
	chunks int
	every  time.Duration
}

func (b *tricklingBody) Read(p []byte) (int, error) {
	if b.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(b.every)
	b.chunks--
	return copy(p, "x"), nil
}

func TestASlowUploadIsNotAStall(t *testing.T) {
	rt, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		w.Write(bytes.Repeat([]byte("y"), int(n)))
	}), 200*time.Millisecond)

	req, _ := http.NewRequest(http.MethodPost, url, io.NopCloser(&tricklingBody{chunks: 10, every: 60 * time.Millisecond}))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("an upload that kept sending for 3x the window failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) != 10 {
		t.Fatalf("body %q, err %v", body, err)
	}
}

func TestACallerCancelIsNotAStall(t *testing.T) {
	rt, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold(t, r)
	}), time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _, err := get(ctx, rt, url)
	if errors.Is(err, ErrStalled) {
		t.Fatalf("a caller's cancel reported as a stall: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
