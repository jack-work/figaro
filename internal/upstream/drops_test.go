package upstream

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) lines(msg string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, msg) {
			out = append(out, line)
		}
	}
	return out
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func okServer(t *testing.T) *httptestServer {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	return &httptestServer{srv.URL, srv.Listener.Addr().String(), serverTLS(srv)}
}

func TestEachDroppedConnectionIsLoggedOnceAndCountedByReason(t *testing.T) {
	log := captureLog(t)
	srv := okServer(t)
	hole := newBlackhole(t, srv.addr)
	rt := New(Config{
		SendPingTimeout: 100 * time.Millisecond,
		PingTimeout:     200 * time.Millisecond,
		MaxAge:          time.Hour,
		Dial:            hole.dial,
		TLS:             srv.tls,
	})

	mustGet(t, rt, srv.url)
	rt.CloseIdleConnections()
	waitDrop(t, rt, ReasonIdle)

	mustGet(t, rt, srv.url)
	hole.cut()
	waitDrop(t, rt, ReasonPeer)

	mustGet(t, rt, srv.url)
	hole.freeze()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := get(ctx, rt, srv.url); err == nil {
		t.Fatal("a request over a blackholed connection succeeded")
	}
	waitDrop(t, rt, ReasonInFlight)

	mustGet(t, rt, srv.url)
	rt.pool.maxAge = time.Nanosecond
	rt.pool.retireOld(time.Now())
	waitDrop(t, rt, ReasonAge)

	st := rt.Stats()
	if st.Open != 0 {
		t.Fatalf("%d connections open, want 0", st.Open)
	}
	var total uint64
	for _, n := range st.Dropped {
		total += n
	}
	if total != 4 {
		t.Fatalf("dropped %v, want one of each of four reasons", st.Dropped)
	}
	lines := log.lines("provider connection dropped")
	if len(lines) != 4 {
		t.Fatalf("%d log lines for 4 drops:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{ReasonIdle, ReasonPeer, ReasonInFlight, ReasonAge} {
		found := false
		for _, l := range lines {
			if strings.Contains(l, `reason="`+want+`"`) || strings.Contains(l, "reason="+want+" ") {
				found = true
			}
		}
		if !found {
			t.Errorf("no log line with reason %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func mustGet(t *testing.T, rt http.RoundTripper, url string) {
	t.Helper()
	if _, _, err := get(context.Background(), rt, url); err != nil {
		t.Fatal(err)
	}
}

func waitDrop(t *testing.T, rt *Transport, reason string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for rt.Stats().Dropped[reason] != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("no drop for %q; drops so far %v", reason, rt.Stats().Dropped)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
