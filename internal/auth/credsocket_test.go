package auth

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func unixCredServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "figcred-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "inst.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// CredSocket caches until 30s before expires_at and re-fetches once within the
// 30s skew window.
func TestCredSocketCachesUntilExpirySkew(t *testing.T) {
	now := time.Unix(1794860000, 0)
	var hits atomic.Int32
	sock := unixCredServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/cred/anthropic-work" {
			http.NotFound(w, r)
			return
		}
		n := hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "anthropic-work",
			"token":      "tok-" + string(rune('0'+n)),
			"expires_at": now.Add(5 * time.Minute).Unix(),
		})
	})

	cs := &CredSocket{
		Socket: sock,
		Name:   "anthropic-work",
		Now:    func() time.Time { return now },
	}
	tok, err := cs.Resolve()
	if err != nil || tok != "tok-1" {
		t.Fatalf("first Resolve = %q, %v", tok, err)
	}

	now = now.Add(4*time.Minute + 29*time.Second)
	tok, err = cs.Resolve()
	if err != nil || tok != "tok-1" || hits.Load() != 1 {
		t.Fatalf("cached Resolve = %q, %v, hits = %d", tok, err, hits.Load())
	}

	now = now.Add(2 * time.Second)
	tok, err = cs.Resolve()
	if err != nil || tok != "tok-2" || hits.Load() != 2 {
		t.Fatalf("post-skew Resolve = %q, %v, hits = %d", tok, err, hits.Load())
	}
}

// SendRenewing re-fetches from the cred socket once when the upstream rejects
// a cached token with 401.
func TestCredSocketRefetchesOnceOnUpstream401(t *testing.T) {
	now := time.Unix(1794860000, 0)
	var hits atomic.Int32
	tokens := []string{"stale-tok", "fresh-tok", "unused-tok"}
	sock := unixCredServer(t, func(w http.ResponseWriter, r *http.Request) {
		idx := int(hits.Add(1)) - 1
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "anthropic-work",
			"token":      tokens[min(idx, len(tokens)-1)],
			"expires_at": now.Add(10 * time.Minute).Unix(),
		})
	})

	cs := &CredSocket{
		Socket: sock,
		Name:   "anthropic-work",
		Now:    func() time.Time { return now },
	}
	var sent []string
	resp, err := SendRenewing(cs, gate("fresh-tok", &sent))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %v, err = %v", resp, err)
	}
	if strings.Join(sent, ",") != "stale-tok,fresh-tok" || hits.Load() != 2 {
		t.Fatalf("sent = %v, hits = %d", sent, hits.Load())
	}

	var cachedSent []string
	resp2, err := SendRenewing(cs, gate("fresh-tok", &cachedSent))
	if err != nil || resp2.StatusCode != http.StatusOK || hits.Load() != 2 {
		t.Fatalf("subsequent request did not reuse fresh-tok: hits = %d, err = %v", hits.Load(), err)
	}
}

// Non-200 error bodies surface socket, name, status, and clean reason, while
// never echoing arbitrary HTML or leaked secrets.
func TestCredSocketSurfacesStructuredErrorsAndRedactsArbitraryBodies(t *testing.T) {
	const plantedSecret = "sk-planted-cred-secret-do-not-echo"
	cases := []struct {
		name       string
		status     int
		body       string
		wantSubstr []string
	}{
		{
			name:       "forbidden peer",
			status:     http.StatusForbidden,
			body:       `{"error":"forbidden","reason":"peer not allowed"}`,
			wantSubstr: []string{"anthropic-work", "HTTP 403", "peer not allowed"},
		},
		{
			name:       "unknown credential",
			status:     http.StatusNotFound,
			body:       `{"error":"unknown_credential","reason":"no such credential"}`,
			wantSubstr: []string{"anthropic-work", "HTTP 404", "no such credential"},
		},
		{
			name:       "stale credential",
			status:     http.StatusServiceUnavailable,
			body:       `{"error":"credential_stale","reason":"sign-in needed"}`,
			wantSubstr: []string{"anthropic-work", "HTTP 503", "sign-in needed", "hush cred login anthropic-work"},
		},
		{
			name:       "raw html with secret",
			status:     http.StatusBadGateway,
			body:       "<html><body>gateway failed: " + plantedSecret + "</body></html>",
			wantSubstr: []string{"anthropic-work", "HTTP 502"},
		},
		{
			name:       "json with leaked token field",
			status:     http.StatusInternalServerError,
			body:       `{"token":"` + plantedSecret + `","error":"internal"}`,
			wantSubstr: []string{"anthropic-work", "HTTP 500", "internal"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := unixCredServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			cs := &CredSocket{Socket: sock, Name: "anthropic-work"}
			_, err := cs.Resolve()
			if err == nil {
				t.Fatal("expected Resolve error")
			}
			msg := err.Error()
			if !strings.Contains(msg, sock) {
				t.Fatalf("error %q missing socket path %q", msg, sock)
			}
			for _, sub := range tc.wantSubstr {
				if !strings.Contains(msg, sub) {
					t.Fatalf("error %q missing %q", msg, sub)
				}
			}
			if strings.Contains(msg, plantedSecret) {
				t.Fatalf("error %q leaked planted secret", msg)
			}
		})
	}
}

func TestCredSocketRejectsMissingSocketAndEmptyToken(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sock")
	cs := &CredSocket{Socket: missing, Name: "inst"}
	if _, err := cs.Resolve(); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing socket err = %v", err)
	}
	if status := cs.ProbeStatus(); !strings.Contains(status, "socket not answering") {
		t.Fatalf("ProbeStatus on missing socket = %q", status)
	}

	emptySock := unixCredServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"inst","token":""}`)
	})
	csEmpty := &CredSocket{Socket: emptySock, Name: "inst"}
	if _, err := csEmpty.Resolve(); err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("empty token err = %v", err)
	}
}

func TestCredSocketProbeNeverExposesToken(t *testing.T) {
	const secret = "sk-ant-oat01-live-secret-token"
	sock := unixCredServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "anthropic-work",
			"token":      secret,
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	})
	cs := &CredSocket{Socket: sock, Name: "anthropic-work"}
	got := cs.ProbeStatus()
	if got != "ready" || strings.Contains(got, secret) {
		t.Fatalf("ProbeStatus = %q", got)
	}
}
