package upstream

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestADeadHTTP2ConnectionFailsAndTheNextRequestDialsFresh(t *testing.T) {
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	hole := newBlackhole(t, srv.Listener.Addr().String())
	rt := New(Config{
		SendPingTimeout: 100 * time.Millisecond,
		PingTimeout:     200 * time.Millisecond,
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
	if n := hole.dialed.Load(); n != 2 {
		t.Fatalf("dialled %d connections, want 2 (the dead one, then a fresh one)", n)
	}
}

func TestSharedCarriesTheHealthCheck(t *testing.T) {
	tr, ok := Shared().(*http.Transport)
	if !ok {
		t.Skip("Shared is wrapped; the round-trip tests cover it")
	}
	if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != Defaults.SendPingTimeout || tr.HTTP2.PingTimeout != Defaults.PingTimeout {
		t.Fatalf("shared transport has no HTTP/2 health check: %+v", tr.HTTP2)
	}
}

// Every client that talks to a model provider must ride the shared transport:
// http.DefaultTransport has no health check, and one dead pooled connection
// hangs every request after a network switch.
func TestNoProviderClientUsesTheDefaultTransport(t *testing.T) {
	roots := []string{"../provider", "../decide"}
	defaults := regexp.MustCompile(`http\.Default(Transport|Client)`)
	client := regexp.MustCompile(`http\.Client\{`)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(src)
			if loc := defaults.FindStringIndex(text); loc != nil {
				t.Errorf("%s:%d uses %s", path, lineOf(text, loc[0]), text[loc[0]:loc[1]])
			}
			for _, loc := range client.FindAllStringIndex(text, -1) {
				end := strings.Index(text[loc[1]:], "\n}")
				if end < 0 {
					end = len(text) - loc[1]
				}
				if !strings.Contains(text[loc[1]:loc[1]+end], "Transport:") {
					t.Errorf("%s:%d builds an http.Client with no Transport (it falls back to http.DefaultTransport)", path, lineOf(text, loc[0]))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func lineOf(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}
