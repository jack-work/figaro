package auth

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSessionCacheExchangesOncePerKey(t *testing.T) {
	c := NewSessionCache()
	var calls int
	exchange := func() (Session, error) {
		calls++
		return Session{Token: "sess-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}

	for i := 0; i < 5; i++ {
		got, err := c.Resolve("k", exchange)
		if err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		if got.Token != "sess-1" {
			t.Fatalf("resolve %d: token = %q", i, got.Token)
		}
	}
	if calls != 1 {
		t.Fatalf("exchanges = %d, want 1: a cached session was re-minted", calls)
	}
}

func TestSessionCacheKeysAreIndependent(t *testing.T) {
	c := NewSessionCache()
	mint := func(tok string) ExchangeFunc {
		return func() (Session, error) {
			return Session{Token: tok, ExpiresAt: time.Now().Add(time.Hour)}, nil
		}
	}
	a, _ := c.Resolve("copilot|github.com|exchange", mint("A"))
	b, _ := c.Resolve("copilot|acme.example|exchange", mint("B"))
	if a.Token == b.Token {
		t.Fatal("two credentials collapsed onto one session")
	}
}

// A refresh storm is the failure this cache exists to prevent: when N arias
// wake to an expired token at once, GitHub must see one request, not N.
func TestSessionCacheSingleFlight(t *testing.T) {
	c := NewSessionCache()
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	exchange := func() (Session, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release // hold the flight open so every caller piles up behind it
		return Session{Token: "sess", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}

	const n = 24
	var wg sync.WaitGroup
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := c.Resolve("k", exchange)
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			tokens[i] = s.Token
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("exchanges = %d, want 1: %d concurrent misses stampeded", calls, n)
	}
	for i, tok := range tokens {
		if tok != "sess" {
			t.Fatalf("goroutine %d got %q", i, tok)
		}
	}
}

func TestSessionCacheRefreshesExpired(t *testing.T) {
	c := NewSessionCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	var calls int
	exchange := func() (Session, error) {
		calls++
		return Session{Token: "sess", ExpiresAt: now.Add(time.Minute)}, nil
	}
	if _, err := c.Resolve("k", exchange); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.Resolve("k", exchange); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("exchanges = %d, want 2: an expired session was reused", calls)
	}
}

func TestSessionCacheInvalidate(t *testing.T) {
	c := NewSessionCache()
	var calls int
	exchange := func() (Session, error) {
		calls++
		return Session{Token: "sess", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	if _, err := c.Resolve("k", exchange); err != nil {
		t.Fatal(err)
	}

	// A straggler 401 naming a token nobody holds any more must not throw
	// away the good one the rest of the fleet is using.
	c.Invalidate("k", "some-older-token")
	if _, err := c.Resolve("k", exchange); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("exchanges = %d, want 1: a stale rejection evicted a live session", calls)
	}

	c.Invalidate("k", "sess")
	if _, err := c.Resolve("k", exchange); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("exchanges = %d, want 2: a rejected session was served again", calls)
	}
}

func TestSessionCacheExchangeErrorIsNotCached(t *testing.T) {
	c := NewSessionCache()
	boom := errors.New("copilot token exchange 403")
	var calls int
	exchange := func() (Session, error) {
		calls++
		if calls == 1 {
			return Session{}, boom
		}
		return Session{Token: "sess", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	if _, err := c.Resolve("k", exchange); !errors.Is(err, boom) {
		t.Fatalf("first resolve err = %v, want the exchange error verbatim", err)
	}
	s, err := c.Resolve("k", exchange)
	if err != nil || s.Token != "sess" {
		t.Fatalf("retry after a transient failure: %v / %q", err, s.Token)
	}
}

func TestSnapshotCarriesNoSecret(t *testing.T) {
	c := NewSessionCache()
	c.Bind("k")
	c.Bind("k")
	if _, err := c.Resolve("k", func() (Session, error) {
		return Session{Token: "super-secret", ExpiresAt: time.Now().Add(time.Hour), Endpoint: "https://api.example"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	snap := c.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot rows = %d", len(snap))
	}
	got := snap[0]
	if got.Bindings != 2 {
		t.Fatalf("bindings = %d, want 2", got.Bindings)
	}
	if got.Exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1", got.Exchanges)
	}
	if got.Fingerprint == "super-secret" || got.Fingerprint == "" {
		t.Fatalf("fingerprint = %q: must be a hash, and must exist", got.Fingerprint)
	}
	if got.Fingerprint != Fingerprint("super-secret") {
		t.Fatal("fingerprint does not identify the token it stands for")
	}
}
