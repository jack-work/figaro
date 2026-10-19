package authz

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

func TestPeercredAdmitsTheDaemonsUidAndMappedUidsOnly(t *testing.T) {
	p, err := NewPeercred(1000, config.PeercredConfig{UIDs: map[string]string{"1001": "sandbox:dev-3"}})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := p.Subject(1000); err != nil || s.Principal != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}) {
		t.Fatalf("self: %+v %v", s, err)
	}
	if s, err := p.Subject(1001); err != nil || s.Principal != (jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "dev-3"}) {
		t.Fatalf("mapped: %+v %v", s, err)
	}
	if _, err := p.Subject(1002); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("unmapped: %v", err)
	}
}

func TestPeercredMayRenameTheDaemonsOwnUid(t *testing.T) {
	p, err := NewPeercred(1000, config.PeercredConfig{UIDs: map[string]string{"1000": "user:gluck"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Self() != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck"}) {
		t.Fatalf("%+v", p.Self())
	}
}

func TestPeercredConfigErrors(t *testing.T) {
	for name, uids := range map[string]map[string]string{
		"not a uid":      {"gluck": "user:gluck"},
		"bad principal":  {"1001": "dev-3"},
		"a foreign node": {"1001": "spain2/user:1001"},
	} {
		if _, err := NewPeercred(1000, config.PeercredConfig{UIDs: uids}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

const (
	webDoor  = "/run/figaro/web.sock"
	kelliher = "https://auth.kelliher.info"
)

func TestUpstreamReadsTheEdgesHeaders(t *testing.T) {
	u, err := NewUpstream(config.UpstreamConfig{Issuer: kelliher, Services: map[string]string{"sandbox-dev-3": "sandbox"}}, webDoor, "/run/figaro/angelus.sock", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Remote-User", "gluck")
	h.Set("Remote-Groups", "figaro-admin, site-fig-access")
	s, err := u.Subject(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if s.Principal != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck@auth.kelliher.info"}) || !slices.Equal(s.Groups, []string{"figaro-admin@auth.kelliher.info", "site-fig-access@auth.kelliher.info"}) {
		t.Fatalf("human: %+v", s)
	}

	h = http.Header{}
	h.Set("Remote-User", "sandbox-dev-3")
	h.Set("Remote-Principal", "service")
	h.Set("Remote-Client", "sandbox-dev-3")
	h.Set("Remote-Groups", "figaro-admin")
	s, err = u.Subject(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if s.Principal != (jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "sandbox-dev-3@auth.kelliher.info"}) || len(s.Groups) != 0 {
		t.Fatalf("a service gets its configured kind and never groups: %+v", s)
	}
}

func TestUpstreamRefuses(t *testing.T) {
	u, _ := NewUpstream(config.UpstreamConfig{Issuer: kelliher, Services: map[string]string{"sandbox-dev-3": "sandbox"}}, webDoor, "", nil)
	for name, set := range map[string]map[string]string{
		"no user":              {},
		"an unlisted service":  {"Remote-User": "kcal-notify", "Remote-Principal": "service", "Remote-Client": "kcal-notify"},
		"a service as another": {"Remote-User": "gluck", "Remote-Principal": "service", "Remote-Client": "sandbox-dev-3"},
		"an unknown kind":      {"Remote-User": "gluck", "Remote-Principal": "root"},
	} {
		h := http.Header{}
		for k, v := range set {
			h.Set(k, v)
		}
		if s, err := u.Subject(context.Background(), h); !errors.Is(err, ErrUnadmitted) {
			t.Errorf("%s: %+v %v", name, s, err)
		}
	}
}

type testIssuer struct {
	t     testing.TB
	url   string
	key   *rsa.PrivateKey
	now   atomic.Int64
	jwks  *httptest.Server
	ui    *httptest.Server
	mu    sync.Mutex
	users map[string]map[string]any
	uiHit atomic.Int32
}

func newTestIssuer(t testing.TB) *testIssuer { return newTestIssuerAt(t, kelliher) }

func newTestIssuerAt(t testing.TB, url string) *testIssuer {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &testIssuer{t: t, url: url, key: k, users: map[string]map[string]any{}}
	is.now.Store(time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC).Unix())
	is.jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	}))
	is.ui = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.uiHit.Add(1)
		is.mu.Lock()
		defer is.mu.Unlock()
		body, ok := is.users[r.Header.Get("Authorization")]
		if !ok {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(is.jwks.Close)
	t.Cleanup(is.ui.Close)
	return is
}

func (is *testIssuer) clock() time.Time { return time.Unix(is.now.Load(), 0) }

func (is *testIssuer) advance(d time.Duration) { is.now.Add(int64(d / time.Second)) }

func (is *testIssuer) mint(sub, client string, ttl time.Duration, user map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: is.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		is.t.Fatal(err)
	}
	now := is.clock()
	raw, err := josejwt.Signed(signer).Claims(map[string]any{
		"iss": is.url, "sub": sub, "client_id": client,
		"iat": now.Unix(), "exp": now.Add(ttl).Unix(),
	}).Serialize()
	if err != nil {
		is.t.Fatal(err)
	}
	if user != nil {
		is.mu.Lock()
		is.users["Bearer "+raw] = user
		is.mu.Unlock()
	}
	return raw
}

func (is *testIssuer) bearer(t testing.TB) *Issuers {
	b, err := NewIssuers([]config.IssuerConfig{{
		Issuer:   is.url,
		JWKS:     is.jwks.URL,
		Userinfo: is.ui.URL,
		Users:    []string{"figaro-cli"},
		Clients:  map[string]string{"sandbox-dev-3": "sandbox"},
	}}, nil, is.clock)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestABearerSessionBindsThePrincipalAtConnect(t *testing.T) {
	is := newTestIssuer(t)
	b := is.bearer(t)
	ctx := context.Background()

	s, err := b.Open(ctx, is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck", "groups": []string{"figaro-admin"}}))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.Subject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Principal != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck@auth.kelliher.info"}) || !sub.InGroup("figaro-admin@auth.kelliher.info") {
		t.Fatalf("%+v", sub)
	}

	box, err := b.Open(ctx, is.mint("sandbox-dev-3", "sandbox-dev-3", time.Hour, nil))
	if err != nil {
		t.Fatal(err)
	}
	if box.Principal() != (jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "sandbox-dev-3@auth.kelliher.info"}) {
		t.Fatalf("%+v", box.Principal())
	}
}

func TestAClientTheConfigDoesNotAdmitIsRefusedAtConnect(t *testing.T) {
	is := newTestIssuer(t)
	if _, err := is.bearer(t).Open(context.Background(), is.mint("u-1", "gluck-todo-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})); err == nil {
		t.Fatal("a token minted for another service opened a session")
	}
}

// The connection must not outlive what it verified: past exp every call is
// refused until a fresh token is presented.
func TestASessionStopsAtExpiryUntilAFreshTokenIsPresented(t *testing.T) {
	is := newTestIssuer(t)
	b := is.bearer(t)
	ctx := context.Background()
	user := map[string]any{"sub": "u-1", "preferred_username": "gluck"}
	s, err := b.Open(ctx, is.mint("u-1", "figaro-cli", 5*time.Minute, user))
	if err != nil {
		t.Fatal(err)
	}
	is.advance(5 * time.Minute)
	if _, err := s.Subject(ctx); !errors.Is(err, ErrExpired) {
		t.Fatalf("served past exp: %v", err)
	}
	if err := s.Present(ctx, is.mint("u-1", "figaro-cli", time.Hour, user)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subject(ctx); err != nil {
		t.Fatalf("after a fresh token: %v", err)
	}
}

func TestAFreshTokenForSomeoneElseCannotTakeOverTheConnection(t *testing.T) {
	is := newTestIssuer(t)
	b := is.bearer(t)
	ctx := context.Background()
	s, err := b.Open(ctx, is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"}))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Present(ctx, is.mint("u-2", "figaro-cli", time.Hour, map[string]any{"sub": "u-2", "preferred_username": "mallory"}))
	if !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("%v", err)
	}
	if sub, err := s.Subject(ctx); err != nil || sub.Principal.ID != "gluck@auth.kelliher.info" {
		t.Fatalf("%+v %v", sub, err)
	}
}

// Groups are re-read per call once the userinfo cache lapses, so a group
// removed upstream stops working on a long-lived connection.
func TestGroupsAreReresolvedOnTheConnectionWhenTheCacheLapses(t *testing.T) {
	is := newTestIssuer(t)
	b := is.bearer(t)
	ctx := context.Background()
	user := map[string]any{"sub": "u-1", "preferred_username": "gluck", "groups": []string{"figaro-admin"}}
	s, err := b.Open(ctx, is.mint("u-1", "figaro-cli", time.Hour, user))
	if err != nil {
		t.Fatal(err)
	}
	is.mu.Lock()
	user["groups"] = []string{}
	is.mu.Unlock()
	if sub, _ := s.Subject(ctx); !sub.InGroup("figaro-admin@auth.kelliher.info") {
		t.Fatal("inside the ttl the cached groups hold")
	}
	is.advance(time.Minute)
	sub, err := s.Subject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sub.InGroup("figaro-admin@auth.kelliher.info") {
		t.Fatal("a removed group survived the cache")
	}
}

func TestIssuerConfigErrors(t *testing.T) {
	base := config.IssuerConfig{Issuer: "https://a", JWKS: "http://j", Clients: map[string]string{"x": "sandbox"}}
	for name, mod := range map[string]func(*config.IssuerConfig){
		"no clients":             func(c *config.IssuerConfig) { c.Clients = nil },
		"users without userinfo": func(c *config.IssuerConfig) { c.Users = []string{"figaro-cli"} },
		"a bad kind":             func(c *config.IssuerConfig) { c.Clients = map[string]string{"x": "ro:ot"} },
		"no scheme":              func(c *config.IssuerConfig) { c.Issuer = "auth.kelliher.info" },
		"a query":                func(c *config.IssuerConfig) { c.Issuer = "https://a/?realm=x" },
		"a user":                 func(c *config.IssuerConfig) { c.Issuer = "https://me@a" },
		"one client both ways": func(c *config.IssuerConfig) {
			c.Userinfo = "http://u"
			c.Users = []string{"x"}
			c.Clients = map[string]string{"x": "sandbox"}
		},
	} {
		c := base
		mod(&c)
		if _, err := NewIssuers([]config.IssuerConfig{c}, nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func BenchmarkSessionSubject(b *testing.B) {
	is := newTestIssuer(b)
	br := is.bearer(b)
	ctx := context.Background()
	s, err := br.Open(ctx, is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"}))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Subject(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBearerOpen(b *testing.B) {
	is := newTestIssuer(b)
	br := is.bearer(b)
	ctx := context.Background()
	tok := is.mint("sandbox-dev-3", "sandbox-dev-3", time.Hour, nil)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := br.Open(ctx, tok); err != nil {
			b.Fatal(err)
		}
	}
}

// Headers are only as honest as the socket they arrive on. The upstream
// authenticator takes the web door's own path and refuses the CLI door,
// where any local caller could set them.
func TestUpstreamIsNeverBoundToTheCLIDoor(t *testing.T) {
	cli := "/run/figaro/angelus.sock"
	for name, c := range map[string]struct {
		listen string
		cfg    config.UpstreamConfig
	}{
		"relative":                {"figaro/web.sock", config.UpstreamConfig{}},
		"empty":                   {"", config.UpstreamConfig{}},
		"the cli door":            {"/run/figaro/../figaro/angelus.sock", config.UpstreamConfig{}},
		"reverify without bearer": {webDoor, config.UpstreamConfig{Issuer: kelliher, Reverify: true}},
		"no issuer":               {webDoor, config.UpstreamConfig{}},
	} {
		if _, err := NewUpstream(c.cfg, c.listen, cli, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	u, err := NewUpstream(config.UpstreamConfig{Issuer: kelliher}, webDoor, cli, nil)
	if err != nil || u.Listener() != webDoor {
		t.Fatalf("%v %v", u, err)
	}
}

func reverifying(t *testing.T, is *testIssuer) *Upstream {
	t.Helper()
	u, err := NewUpstream(config.UpstreamConfig{Issuer: is.url, Reverify: true, Services: map[string]string{"sandbox-dev-3": "sandbox"}}, webDoor, "", is.bearer(t))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func edge(user, kind, client, token string) http.Header {
	h := http.Header{}
	h.Set("Remote-User", user)
	if kind != "" {
		h.Set("Remote-Principal", kind)
	}
	if client != "" {
		h.Set("Remote-Client", client)
	}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

func TestReverifyingAcceptsATokenThatSaysWhatTheEdgeSaid(t *testing.T) {
	is := newTestIssuer(t)
	u := reverifying(t, is)
	ctx := context.Background()
	human := is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})
	if _, err := u.Subject(ctx, edge("gluck", "human", "figaro-cli", human)); err != nil {
		t.Fatal(err)
	}
	box := is.mint("sandbox-dev-3", "sandbox-dev-3", time.Hour, nil)
	if _, err := u.Subject(ctx, edge("sandbox-dev-3", "service", "sandbox-dev-3", box)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Subject(ctx, edge("gluck", "", "", "")); err != nil {
		t.Fatalf("a browser session carries no token and needs none: %v", err)
	}
}

func TestReverifyingRefuses(t *testing.T) {
	is := newTestIssuer(t)
	u := reverifying(t, is)
	ctx := context.Background()
	mallory := is.mint("u-2", "figaro-cli", time.Hour, map[string]any{"sub": "u-2", "preferred_username": "mallory"})
	for name, h := range map[string]http.Header{
		"headers and token disagree": edge("gluck", "human", "figaro-cli", mallory),
		"a bearer the edge withheld": edge("gluck", "human", "figaro-cli", ""),
		"a forged token":             edge("gluck", "human", "figaro-cli", "eyJhbGciOiJub25lIn0.e30."),
	} {
		if _, err := u.Subject(ctx, h); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
}

// A caller is forwarded to another node with the token it proved itself
// with, and only that: a peercred caller or a tokenless browser has none
// to carry, and the edge's headers are never turned into one.
func TestOnlyAVerifiedTokenIsForwardable(t *testing.T) {
	is := newTestIssuer(t)
	b := is.bearer(t)
	ctx := context.Background()
	tok := is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})

	r := &http.Request{Header: http.Header{"Authorization": {"Bearer " + tok}}}
	bctx, err := b.AdmitRequest(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := BearerOf(bctx); !ok || got != tok {
		t.Fatalf("bearer door: %q %v", got, ok)
	}

	u := reverifying(t, is)
	uctx, err := u.AdmitRequest(ctx, &http.Request{Header: edge("gluck", "human", "figaro-cli", tok)})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := BearerOf(uctx); !ok || got != tok {
		t.Fatalf("upstream, reverified: %q %v", got, ok)
	}
	uctx, err = u.AdmitRequest(ctx, &http.Request{Header: edge("gluck", "", "", "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := BearerOf(uctx); ok {
		t.Fatal("a browser session became forwardable")
	}
	if _, ok := BearerOf(WithPrincipal(ctx, rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"})); ok {
		t.Fatal("a peercred caller became forwardable")
	}
}

func (is *testIssuer) config() config.IssuerConfig {
	return config.IssuerConfig{
		Issuer:   is.url,
		JWKS:     is.jwks.URL,
		Userinfo: is.ui.URL,
		Users:    []string{"figaro-cli"},
		Clients:  map[string]string{"sandbox-dev-3": "sandbox"},
	}
}

func twoIssuers(t *testing.T) (*testIssuer, *testIssuer, *Issuers) {
	t.Helper()
	a, b := newTestIssuer(t), newTestIssuerAt(t, "https://id.example.org/realms/guests")
	is, err := NewIssuers([]config.IssuerConfig{a.config(), b.config()}, nil, a.clock)
	if err != nil {
		t.Fatal(err)
	}
	return a, b, is
}

// Two issuers can each have a "gluck"; they are different people and must
// never resolve to the same principal.
func TestTheSameNameFromTwoIssuersIsTwoPrincipals(t *testing.T) {
	a, b, is := twoIssuers(t)
	ctx := context.Background()
	user := map[string]any{"sub": "u-1", "preferred_username": "gluck", "groups": []string{"figaro-admin"}}
	sa, err := is.Open(ctx, a.mint("u-1", "figaro-cli", time.Hour, user))
	if err != nil {
		t.Fatal(err)
	}
	sb, err := is.Open(ctx, b.mint("u-1", "figaro-cli", time.Hour, user))
	if err != nil {
		t.Fatal(err)
	}
	if sa.Principal().String() != "user:gluck@auth.kelliher.info" || sb.Principal().String() != "user:gluck@id.example.org/realms/guests" {
		t.Fatalf("%s %s", sa.Principal(), sb.Principal())
	}
	sub, err := sb.Subject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sub.InGroup("figaro-admin@auth.kelliher.info") || !sub.InGroup("figaro-admin@id.example.org/realms/guests") {
		t.Fatalf("%+v", sub.Groups)
	}
}

// A second issuer may hand out any group name it likes; a binding written
// for the first issuer's group must not be satisfied by it.
func TestAGroupFromTheWrongIssuerSatisfiesNoBinding(t *testing.T) {
	a, b, is := twoIssuers(t)
	ctx := context.Background()
	p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.toml")
	src := `
[roles.admin]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "admin"
groups = ["figaro-admin@auth.kelliher.info"]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	user := map[string]any{"sub": "u-9", "preferred_username": "mallory", "groups": []string{"figaro-admin"}}
	for name, c := range map[string]struct {
		iss  *testIssuer
		want bool
	}{"the bound issuer": {a, true}, "another issuer": {b, false}} {
		s, err := is.Open(ctx, c.iss.mint("u-9", "figaro-cli", time.Hour, user))
		if err != nil {
			t.Fatal(err)
		}
		sub, err := s.Subject(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Allow(sub, rpc.VerbUse, Daemon).Allowed; got != c.want {
			t.Errorf("%s: allowed %v", name, got)
		}
	}
}

func TestATokenFromAnUntrustedIssuerIsRefused(t *testing.T) {
	_, _, two := twoIssuers(t)
	stranger := newTestIssuerAt(t, "https://evil.example")
	tok := stranger.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})
	if _, err := two.Open(context.Background(), tok); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("two issuers: %v", err)
	}
	if _, err := newTestIssuer(t).bearer(t).Open(context.Background(), tok); err == nil {
		t.Fatal("one issuer: admitted")
	}
	if _, err := two.Open(context.Background(), "not-a-jwt"); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("garbage: %v", err)
	}
}

// A session stays with the issuer that opened it: a token from another
// issuer, even for the same name, cannot refresh it.
func TestASessionRefusesATokenFromTheOtherIssuer(t *testing.T) {
	a, b, is := twoIssuers(t)
	ctx := context.Background()
	user := map[string]any{"sub": "u-1", "preferred_username": "gluck"}
	s, err := is.Open(ctx, a.mint("u-1", "figaro-cli", time.Hour, user))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Present(ctx, b.mint("u-1", "figaro-cli", time.Hour, user)); err == nil {
		t.Fatal("another issuer's token took over the session")
	}
	if got := s.Principal().String(); got != "user:gluck@auth.kelliher.info" {
		t.Fatal(got)
	}
}

func TestTwoIssuersWithOneScopeAreRefused(t *testing.T) {
	a := newTestIssuer(t)
	b := newTestIssuerAt(t, "http://auth.kelliher.info/")
	_, err := NewIssuers([]config.IssuerConfig{a.config(), b.config()}, nil, nil)
	if err == nil {
		t.Fatal("two issuers share @auth.kelliher.info")
	} else if !strings.Contains(err.Error(), "both scope") {
		t.Fatal(err)
	}
}

func TestReverifyingRefusesTheSameNameFromAnotherIssuer(t *testing.T) {
	a, b, is := twoIssuers(t)
	u, err := NewUpstream(config.UpstreamConfig{Issuer: a.url, Reverify: true}, webDoor, "", is)
	if err != nil {
		t.Fatal(err)
	}
	tok := b.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})
	if _, err := u.Subject(context.Background(), edge("gluck", "human", "figaro-cli", tok)); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("%v", err)
	}
	if _, err := NewUpstream(config.UpstreamConfig{Issuer: "https://elsewhere", Reverify: true}, webDoor, "", is); err == nil {
		t.Fatal("reverify against an issuer this node does not trust")
	}
}

func BenchmarkSessionSubjectWithGroups(b *testing.B) {
	is := newTestIssuer(b)
	br := is.bearer(b)
	ctx := context.Background()
	s, err := br.Open(ctx, is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck", "groups": []string{"figaro-admin", "troupe-members", "site-fig-access"}}))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Subject(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBearerOpenTwoIssuers(b *testing.B) {
	a, other := newTestIssuer(b), newTestIssuerAt(b, "https://id.example.org/realms/guests")
	is, err := NewIssuers([]config.IssuerConfig{a.config(), other.config()}, nil, a.clock)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	tok := other.mint("sandbox-dev-3", "sandbox-dev-3", time.Hour, nil)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := is.Open(ctx, tok); err != nil {
			b.Fatal(err)
		}
	}
}
