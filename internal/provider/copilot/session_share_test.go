package copilot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
)

// stubGitHub stands in for hush/env: the durable credential.
type stubGitHub struct {
	token    string
	resolves atomic.Int64
}

func (s *stubGitHub) Resolve() (string, error) {
	s.resolves.Add(1)
	return s.token, nil
}
func (s *stubGitHub) Invalidate(string) error { return nil }

// exchangeServer is api.github.com/copilot_internal/v2/token, counting.
func exchangeServer(t *testing.T, ttl time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	var mu sync.Mutex
	seq := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		seq++
		n := seq
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"token":      fmt.Sprintf("tid=abc;exp=%d;proxy-ep=proxy.example.com;n=%d", time.Now().Add(ttl).Unix(), n),
			"expires_at": time.Now().Add(ttl).Unix(),
			"endpoints":  map[string]string{"api": "https://api.example.com"},
		})
	}))
	t.Cleanup(srv.Close)

	prev := exchangeEndpoint
	exchangeEndpoint = func(string) string { return srv.URL }
	t.Cleanup(func() { exchangeEndpoint = prev })
	return srv, &hits
}

// TWO ARIAS, ONE SESSION TOKEN.
//
// This is the property the shared cache exists for, tested through the path
// production takes: New() is what internal/figaro/provbind.go calls once per
// agent, so two New()s are two arias. Before the SessionCache they made two
// exchanges against GitHub and held two unrelated tokens; the fleet's cost
// scaled with conversations, and the burst is what earned the 403 that
// figaro then reported as "no provider connected".
func TestTwoAriasShareOneSessionToken(t *testing.T) {
	_, hits := exchangeServer(t, time.Hour)
	sessions := auth.NewSessionCache()
	gh := &stubGitHub{token: "gho_durable"}

	first := newTokenSourceIn(sessions, gh, Config{})
	second := newTokenSourceIn(sessions, gh, Config{})

	a, err := first.Resolve()
	if err != nil {
		t.Fatalf("aria 1 resolve: %v", err)
	}
	b, err := second.Resolve()
	if err != nil {
		t.Fatalf("aria 2 resolve: %v", err)
	}

	if a != b {
		t.Fatalf("two arias hold different session tokens:\n  %s\n  %s", auth.Fingerprint(a), auth.Fingerprint(b))
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("token exchanges = %d, want 1: the second aria minted its own", got)
	}
	if got := gh.resolves.Load(); got != 1 {
		t.Fatalf("durable credential reads = %d, want 1: a cache hit must not touch the keystore", got)
	}

	// And the sharing is VISIBLE, which is what `figaro doctor provider`
	// renders and what makes a regression noticeable rather than mystical.
	snap := sessions.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot rows = %d, want 1 credential", len(snap))
	}
	if snap[0].Bindings != 2 || snap[0].Exchanges != 1 {
		t.Fatalf("snapshot = %d bindings / %d exchanges, want 2 / 1", snap[0].Bindings, snap[0].Exchanges)
	}
	if snap[0].Fingerprint != auth.Fingerprint(a) {
		t.Fatal("snapshot fingerprint does not name the token both arias hold")
	}

	// The routing discovered by one aria's exchange serves the other too.
	if first.BaseURL() != "https://api.example.com" || second.BaseURL() != first.BaseURL() {
		t.Fatalf("base URLs diverged: %q vs %q", first.BaseURL(), second.BaseURL())
	}
}

// The same property through the full provider constructor, so nothing
// between New() and the token source can quietly reintroduce a private cache.
func TestTwoProvidersShareOneSessionToken(t *testing.T) {
	_, hits := exchangeServer(t, time.Hour)
	prev := auth.Sessions
	auth.Sessions = auth.NewSessionCache()
	t.Cleanup(func() { auth.Sessions = prev })

	gh := &stubGitHub{token: "gho_durable"}
	build := func() *Copilot {
		p, err := New(provider.Knobs{Model: "claude-sonnet-4.5", MaxTokens: 1000}, gh, Config{}, nil, nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}

	one, two := build(), build()
	a, err := one.tokenSrc.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	b, err := two.tokenSrc.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("two provider instances hold different session tokens")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("token exchanges = %d, want 1", got)
	}
}

// A fleet waking together must cost one exchange, not one per aria.
func TestSessionRefreshStormIsOneExchange(t *testing.T) {
	_, hits := exchangeServer(t, time.Hour)
	sessions := auth.NewSessionCache()
	gh := &stubGitHub{token: "gho_durable"}

	const arias = 16
	srcs := make([]*CopilotTokenSource, arias)
	for i := range srcs {
		srcs[i] = newTokenSourceIn(sessions, gh, Config{})
	}

	var wg sync.WaitGroup
	got := make([]string, arias)
	for i := range srcs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := srcs[i].Resolve()
			if err != nil {
				t.Errorf("aria %d: %v", i, err)
				return
			}
			got[i] = tok
		}(i)
	}
	wg.Wait()

	if n := hits.Load(); n != 1 {
		t.Fatalf("token exchanges = %d, want 1: %d arias stampeded the exchange endpoint", n, arias)
	}
	for i, tok := range got {
		if tok != got[0] {
			t.Fatalf("aria %d holds a different token from aria 0", i)
		}
	}
}

// A rejected session heals the whole fleet with one exchange, and the
// durable credential is refreshed rather than the aria being told it has
// no credential at all.
func TestInvalidateSharesTheRepair(t *testing.T) {
	_, hits := exchangeServer(t, time.Hour)
	sessions := auth.NewSessionCache()
	gh := &stubGitHub{token: "gho_durable"}

	first := newTokenSourceIn(sessions, gh, Config{})
	second := newTokenSourceIn(sessions, gh, Config{})

	a, err := first.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Invalidate(a); err != nil {
		t.Fatal(err)
	}

	b, err := second.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if b == a {
		t.Fatal("a rejected session was handed to the next caller")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("token exchanges = %d, want 2 (initial + repair)", n)
	}
	c, err := first.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if c != b {
		t.Fatal("the repair was not shared: each source re-minted its own")
	}
}

// Different credentials must not collapse onto one another: an enterprise
// domain is a different GitHub, and direct mode is a different credential
// entirely.
func TestSessionKeySeparatesCredentials(t *testing.T) {
	sessions := auth.NewSessionCache()
	gh := &stubGitHub{token: "gho_durable"}

	dotcom := newTokenSourceIn(sessions, gh, Config{})
	enterprise := newTokenSourceIn(sessions, gh, Config{EnterpriseDomain: "acme.example"})
	direct := newTokenSourceIn(sessions, gh, Config{TokenMode: "direct"})

	if dotcom.key == enterprise.key {
		t.Fatalf("dotcom and enterprise share a session key (%q)", dotcom.key)
	}
	if !strings.Contains(direct.key, "direct") {
		t.Fatalf("direct mode key = %q", direct.key)
	}

	// Direct mode presents the durable token unchanged: nothing to cache,
	// nothing to share, and no binding claimed.
	tok, err := direct.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "gho_durable" {
		t.Fatalf("direct mode token = %q", auth.Fingerprint(tok))
	}
	if _, ok := sessions.Peek(direct.key); ok {
		t.Fatal("direct mode cached a session it never exchanged")
	}
	if direct.BaseURL() != directBaseURL {
		t.Fatalf("direct base URL = %q", direct.BaseURL())
	}
}
