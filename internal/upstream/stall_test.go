package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock moves only when a test says so: the stall tests assert what the
// watchdog does with a given silence, not how fast a loaded machine runs.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c      *fakeClock
	at     time.Time
	f      func()
	active bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f, active: true}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.active
	t.at, t.active = t.c.now.Add(d), true
	return was
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if t.active && !t.at.After(c.now) {
			t.active = false
			due = append(due, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

const window = time.Second

func stallTransport(t *testing.T, h http.Handler) (*Transport, *fakeClock, string) {
	t.Helper()
	srv := h2Server(t, h)
	rt := New(Config{Stall: window, TLS: serverTLS(srv)})
	clk := newFakeClock()
	rt.clock = clk
	return rt, clk, srv.URL
}

func hold(t *testing.T, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-t.Context().Done():
	}
}

func within(t *testing.T, what string, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: still waiting after 10s", what)
		return nil
	}
}

func TestAStreamThatGoesSilentFailsAsStalled(t *testing.T) {
	rt, clk, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("event: message_start\n\n"))
		w.(http.Flusher).Flush()
		hold(t, r)
	}))

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len("event: message_start\n\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}

	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Body)
		read <- err
	}()
	clk.Advance(window)
	err = within(t, "the read after a silent window", read)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("a silent stream ended with %v, want ErrStalled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("a stall reads as a user cancel: the turn would keep the partial output")
	}
}

func TestNoResponseHeadersFailsAsStalled(t *testing.T) {
	arrived := make(chan struct{})
	rt, clk, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		hold(t, r)
	}))

	done := make(chan error, 1)
	go func() {
		_, _, err := get(context.Background(), rt, url)
		done <- err
	}()
	<-arrived
	clk.Advance(window)
	if err := within(t, "a request with no headers after a silent window", done); !errors.Is(err, ErrStalled) {
		t.Fatalf("got %v, want ErrStalled", err)
	}
}

func TestAStreamLongerThanTheWindowIsNotAStallWhileBytesFlow(t *testing.T) {
	const chunk = "event: ping\n\n"
	next := make(chan struct{})
	rt, clk, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		for range next {
			w.Write([]byte(chunk))
			w.(http.Flusher).Flush()
		}
		w.Write([]byte("done"))
	}))

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, len(chunk))
	for i := 0; i < 10; i++ {
		next <- struct{}{}
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatalf("chunk %d, %v into the stream: %v", i, time.Duration(i)*window/2, err)
		}
		clk.Advance(window / 2)
	}
	close(next)
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("a stream that kept sending for 5x the window failed: %v", err)
	}
	if string(rest) != "done" {
		t.Fatalf("tail %q", rest)
	}
}

type tricklingBody struct {
	chunks int
	clk    *fakeClock
}

func (b *tricklingBody) Read(p []byte) (int, error) {
	if b.chunks == 0 {
		return 0, io.EOF
	}
	b.clk.Advance(window / 2)
	b.chunks--
	return copy(p, "x"), nil
}

func TestASlowUploadIsNotAStall(t *testing.T) {
	rt, clk, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		w.Write(bytes.Repeat([]byte("y"), int(n)))
	}))

	req, _ := http.NewRequest(http.MethodPost, url, io.NopCloser(&tricklingBody{chunks: 10, clk: clk}))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("an upload that kept sending for 5x the window failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) != 10 {
		t.Fatalf("body %q, err %v", body, err)
	}
}

func TestACallerCancelIsNotAStall(t *testing.T) {
	rt, _, url := stallTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold(t, r)
	}))

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

func TestTheWallClockWatchdogFires(t *testing.T) {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		hold(t, r)
	}))
	rt := New(Config{Stall: 50 * time.Millisecond, TLS: serverTLS(srv)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, err := get(ctx, rt, srv.URL)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("got %v, want ErrStalled from the real clock", err)
	}
	if !strings.Contains(err.Error(), "no bytes for 50ms") {
		t.Fatalf("%v does not name the window", err)
	}
}
