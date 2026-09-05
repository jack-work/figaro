package copilot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

// fakeResolver stands in for the hush-backed credential.
type fakeResolver struct {
	token       string
	endpoint    string
	resolves    int
	invalidated string
}

func (f *fakeResolver) Resolve() (string, error) {
	f.resolves++
	return f.token, nil
}

func (f *fakeResolver) Invalidate(tok string) error {
	f.invalidated = tok
	return nil
}

func (f *fakeResolver) Endpoint() string { return f.endpoint }

// The token source holds nothing. Every Resolve asks the resolver, which
// asks hush: that round-trip IS how one session token is shared by every
// aria and every figaro process. A cache here would be a regression, not an
// optimisation.
func TestTokenSourceDoesNotCache(t *testing.T) {
	r := &fakeResolver{token: "sess-1"}
	s := newTokenSource(r, Config{})

	for i := 0; i < 5; i++ {
		got, err := s.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		if got != "sess-1" {
			t.Fatalf("resolve %d = %q", i, got)
		}
	}
	if r.resolves != 5 {
		t.Fatalf("resolver calls = %d, want 5: the token source cached a session it does not own", r.resolves)
	}

	// A rotation at hush is visible immediately, with nothing to evict.
	r.token = "sess-2"
	if got, _ := s.Resolve(); got != "sess-2" {
		t.Fatalf("after rotation = %q, want sess-2", got)
	}
}

func TestInvalidateDelegates(t *testing.T) {
	r := &fakeResolver{token: "sess-1"}
	s := newTokenSource(r, Config{})
	if err := s.Invalidate("sess-1"); err != nil {
		t.Fatal(err)
	}
	if r.invalidated != "sess-1" {
		t.Fatalf("invalidated = %q: the rejection never reached hush", r.invalidated)
	}
}

// Routing is minted WITH the token, so it comes from the same place.
func TestBaseURLPrecedence(t *testing.T) {
	minted := "https://api.enterprise.githubcopilot.com"

	t.Run("explicit override wins", func(t *testing.T) {
		s := newTokenSource(&fakeResolver{token: "t", endpoint: minted}, Config{BaseURL: "https://proxy.local/"})
		if got := s.BaseURL(); got != "https://proxy.local" {
			t.Fatalf("= %q", got)
		}
	})

	t.Run("direct mode has its own host", func(t *testing.T) {
		s := newTokenSource(&fakeResolver{token: "gho_x"}, Config{TokenMode: "direct"})
		if got := s.BaseURL(); got != directBaseURL {
			t.Fatalf("= %q", got)
		}
	})

	t.Run("the exchange's routing is used", func(t *testing.T) {
		s := newTokenSource(&fakeResolver{token: "t", endpoint: minted}, Config{})
		if got := s.BaseURL(); got != minted {
			t.Fatalf("= %q, want the host hush minted with the token", got)
		}
	})

	t.Run("falls back to the token body", func(t *testing.T) {
		r := &fakeResolver{token: "tid=abc;proxy-ep=proxy.example.com;"}
		s := newTokenSource(r, Config{})
		if got := s.BaseURL(); got != "https://api.example.com" {
			t.Fatalf("= %q", got)
		}
		if r.resolves == 0 {
			t.Fatal("routing was guessed without ever asking for a token")
		}
	})
}

// exchangeURL is what tells hush where to mint. Direct mode returns "":
// that installation presents the GitHub token unchanged and there is
// nothing to exchange.
func TestExchangeURL(t *testing.T) {
	loaded := func(cfg Config) *config.Loaded {
		t.Helper()
		return writeProviderConfig(t, cfg)
	}

	if got := exchangeURL(loaded(Config{})); got != "https://api.github.com/copilot_internal/v2/token" {
		t.Fatalf("dotcom = %q", got)
	}
	if got := exchangeURL(loaded(Config{EnterpriseDomain: "acme.example"})); got != "https://api.acme.example/copilot_internal/v2/token" {
		t.Fatalf("enterprise = %q", got)
	}
	if got := exchangeURL(loaded(Config{TokenMode: "direct"})); got != "" {
		t.Fatalf("direct = %q, want empty (no exchange)", got)
	}
}

// The hush-backed resolver is what carries the routing, and a resolver that
// carries none must not be mistaken for one that does.
func TestUnmintedEndpointIsEmpty(t *testing.T) {
	var carrier auth.EndpointCarrier = &auth.OAuth{}
	if got := carrier.Endpoint(); got != "" {
		t.Fatalf("unminted endpoint = %q", got)
	}
}

// writeProviderConfig lays down providers/copilot.toml in a throwaway
// config dir and loads it, so exchangeURL is exercised through the same
// path production takes.
func writeProviderConfig(t *testing.T, cfg Config) *config.Loaded {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "providers"), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "providers", "copilot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}
