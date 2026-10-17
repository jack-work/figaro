package authz

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

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

const webDoor = "/run/figaro/web.sock"

func TestUpstreamReadsTheEdgesHeaders(t *testing.T) {
	u, err := NewUpstream(config.UpstreamConfig{Services: map[string]string{"sandbox-dev-3": "sandbox"}}, webDoor, "/run/figaro/angelus.sock", nil)
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
	if s.Principal != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck"}) || !slices.Equal(s.Groups, []string{"figaro-admin", "site-fig-access"}) {
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
	if s.Principal != (jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "sandbox-dev-3"}) || len(s.Groups) != 0 {
		t.Fatalf("a service gets its configured kind and never groups: %+v", s)
	}
}

func TestUpstreamRefuses(t *testing.T) {
	u, _ := NewUpstream(config.UpstreamConfig{Services: map[string]string{"sandbox-dev-3": "sandbox"}}, webDoor, "", nil)
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

type issuer struct {
	t     testing.TB
	key   *rsa.PrivateKey
	now   atomic.Int64
	jwks  *httptest.Server
	ui    *httptest.Server
	mu    sync.Mutex
	users map[string]map[string]any
	uiHit atomic.Int32
}

func newIssuer(t testing.TB) *issuer {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &issuer{t: t, key: k, users: map[string]map[string]any{}}
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

func (is *issuer) clock() time.Time { return time.Unix(is.now.Load(), 0) }

func (is *issuer) advance(d time.Duration) { is.now.Add(int64(d / time.Second)) }

func (is *issuer) mint(sub, client string, ttl time.Duration, user map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: is.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		is.t.Fatal(err)
	}
	now := is.clock()
	raw, err := josejwt.Signed(signer).Claims(map[string]any{
		"iss": "https://auth.kelliher.info", "sub": sub, "client_id": client,
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

func (is *issuer) bearer(t testing.TB) *Bearer {
	b, err := NewBearer(config.BearerConfig{
		Issuer:   "https://auth.kelliher.info",
		JWKS:     is.jwks.URL,
		Userinfo: is.ui.URL,
		Users:    []string{"figaro-cli"},
		Clients:  map[string]string{"sandbox-dev-3": "sandbox"},
	}, nil, is.clock)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestABearerSessionBindsThePrincipalAtConnect(t *testing.T) {
	is := newIssuer(t)
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
	if sub.Principal != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck"}) || !sub.InGroup("figaro-admin") {
		t.Fatalf("%+v", sub)
	}

	box, err := b.Open(ctx, is.mint("sandbox-dev-3", "sandbox-dev-3", time.Hour, nil))
	if err != nil {
		t.Fatal(err)
	}
	if box.Principal() != (jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "sandbox-dev-3"}) {
		t.Fatalf("%+v", box.Principal())
	}
}

func TestAClientTheConfigDoesNotAdmitIsRefusedAtConnect(t *testing.T) {
	is := newIssuer(t)
	if _, err := is.bearer(t).Open(context.Background(), is.mint("u-1", "gluck-todo-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})); err == nil {
		t.Fatal("a token minted for another service opened a session")
	}
}

// The connection must not outlive what it verified: past exp every call is
// refused until a fresh token is presented.
func TestASessionStopsAtExpiryUntilAFreshTokenIsPresented(t *testing.T) {
	is := newIssuer(t)
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
	is := newIssuer(t)
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
	if sub, err := s.Subject(ctx); err != nil || sub.Principal.ID != "gluck" {
		t.Fatalf("%+v %v", sub, err)
	}
}

// Groups are re-read per call once the userinfo cache lapses, so a group
// removed upstream stops working on a long-lived connection.
func TestGroupsAreReresolvedOnTheConnectionWhenTheCacheLapses(t *testing.T) {
	is := newIssuer(t)
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
	if sub, _ := s.Subject(ctx); !sub.InGroup("figaro-admin") {
		t.Fatal("inside the ttl the cached groups hold")
	}
	is.advance(time.Minute)
	sub, err := s.Subject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sub.InGroup("figaro-admin") {
		t.Fatal("a removed group survived the cache")
	}
}

func TestBearerConfigErrors(t *testing.T) {
	base := config.BearerConfig{Issuer: "https://a", JWKS: "http://j"}
	for name, mod := range map[string]func(*config.BearerConfig){
		"no clients":             func(c *config.BearerConfig) {},
		"users without userinfo": func(c *config.BearerConfig) { c.Users = []string{"figaro-cli"} },
		"a bad kind":             func(c *config.BearerConfig) { c.Clients = map[string]string{"x": "ro:ot"} },
		"one client both ways": func(c *config.BearerConfig) {
			c.Userinfo = "http://u"
			c.Users = []string{"x"}
			c.Clients = map[string]string{"x": "sandbox"}
		},
	} {
		c := base
		mod(&c)
		if _, err := NewBearer(c, nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func BenchmarkSessionSubject(b *testing.B) {
	is := newIssuer(b)
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
	is := newIssuer(b)
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
		"reverify without bearer": {webDoor, config.UpstreamConfig{Reverify: true}},
	} {
		if _, err := NewUpstream(c.cfg, c.listen, cli, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	u, err := NewUpstream(config.UpstreamConfig{}, webDoor, cli, nil)
	if err != nil || u.Listener() != webDoor {
		t.Fatalf("%v %v", u, err)
	}
}

func reverifying(t *testing.T, is *issuer) *Upstream {
	t.Helper()
	u, err := NewUpstream(config.UpstreamConfig{Reverify: true, Services: map[string]string{"sandbox-dev-3": "sandbox"}}, webDoor, "", is.bearer(t))
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
	is := newIssuer(t)
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
	is := newIssuer(t)
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
