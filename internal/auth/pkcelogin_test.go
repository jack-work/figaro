package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkceIdP is an issuer with an authorization code endpoint. Its token endpoint
// checks the verifier against the challenge the authorize URL carried, so a
// test passes only if figaro sent a matching pair.
type pkceIdP struct {
	srv       *httptest.Server
	mu        sync.Mutex
	authorize url.Values
	tokenForm url.Values
	noRefresh bool
}

func newPKCEIdP(t *testing.T) *pkceIdP {
	t.Helper()
	p := &pkceIdP{}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 p.srv.URL,
				"authorization_endpoint": p.srv.URL + "/ui/oauth2",
				"token_endpoint":         p.srv.URL + "/oauth2/token",
			})
		case "/oauth2/token":
			require.NoError(t, r.ParseForm())
			p.mu.Lock()
			p.tokenForm = r.PostForm
			challenge := p.authorize.Get("code_challenge")
			p.mu.Unlock()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if base64urlEncode(sum[:]) != challenge || r.PostForm.Get("code") != "the-code" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			out := map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 900}
			if !p.noRefresh {
				out["refresh_token"] = "rt"
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	prev := oidcHTTP
	oidcHTTP = p.srv.Client()
	t.Cleanup(func() { oidcHTTP = prev })
	return p
}

func (p *pkceIdP) config(t *testing.T) PKCEConfig {
	t.Helper()
	cfg, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		Scopes: []string{"openid", "groups", "profile"}}.PKCEConfig("band")
	require.NoError(t, err)
	return cfg
}

// browser plays the user: it records the authorize URL and then follows the
// redirect the IdP would send, with whatever query the test asks for.
func (p *pkceIdP) browser(t *testing.T, redirectQuery func(state string) url.Values) func(string) {
	return func(raw string) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		p.mu.Lock()
		p.authorize = u.Query()
		p.mu.Unlock()
		back, err := url.Parse(u.Query().Get("redirect_uri"))
		require.NoError(t, err)
		back.RawQuery = redirectQuery(u.Query().Get("state")).Encode()
		go func() {
			if resp, err := http.Get(back.String()); err == nil {
				resp.Body.Close()
			}
		}()
	}
}

func codeFor(state string) url.Values { return url.Values{"code": {"the-code"}, "state": {state}} }

func TestPKCEConfigComesFromDiscoveryAndKeepsTheScopes(t *testing.T) {
	p := newPKCEIdP(t)
	cfg := p.config(t)
	assert.Equal(t, p.srv.URL+"/ui/oauth2", cfg.AuthorizeURL)
	assert.Equal(t, p.srv.URL+"/oauth2/token", cfg.TokenURL)
	assert.Equal(t, "openid groups profile", cfg.Scope, "nothing is added: Kanidm needs no offline_access")
}

func TestPKCEConfigRefusesWhatItCannotDo(t *testing.T) {
	p := newPKCEIdP(t)
	base := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE}
	for name, mutate := range map[string]func(*OAuthSettings){
		"the device flow":   func(s *OAuthSettings) { s.Flow = FlowDevice },
		"plain http issuer": func(s *OAuthSettings) { s.Issuer = strings.Replace(s.Issuer, "https://", "http://", 1) },
		"no client id":      func(s *OAuthSettings) { s.ClientID = "" },
	} {
		s := base
		mutate(&s)
		if _, err := s.PKCEConfig("band"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoginPKCEExchangesTheCodeAsAPublicClient(t *testing.T) {
	p := newPKCEIdP(t)
	tok, err := loginPKCE(context.Background(), p.config(t), p.browser(t, codeFor), 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, DeviceTokens{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 900}, tok)

	a := p.authorize
	assert.Equal(t, "code", a.Get("response_type"))
	assert.Equal(t, "rou2", a.Get("client_id"))
	assert.Equal(t, "S256", a.Get("code_challenge_method"))
	assert.Equal(t, "openid groups profile", a.Get("scope"))
	assert.Regexp(t, `^http://127\.0\.0\.1:\d+/callback$`, a.Get("redirect_uri"), "a loopback IP literal, per RFC 8252")
	assert.NotEmpty(t, a.Get("state"))

	f := p.tokenForm
	assert.Equal(t, "authorization_code", f.Get("grant_type"))
	assert.Equal(t, a.Get("redirect_uri"), f.Get("redirect_uri"), "the exchange repeats the redirect it authorized")
	assert.Equal(t, "rou2", f.Get("client_id"))
	assert.Empty(t, f.Get("client_secret"), "a public client sends no secret")
}

func TestLoginPKCEIgnoresARedirectWithTheWrongState(t *testing.T) {
	p := newPKCEIdP(t)
	var forgedStatus int
	open := func(raw string) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		back, err := url.Parse(u.Query().Get("redirect_uri"))
		require.NoError(t, err)
		back.RawQuery = url.Values{"code": {"stolen"}, "state": {"not-ours"}}.Encode()
		resp, err := http.Get(back.String())
		require.NoError(t, err)
		resp.Body.Close()
		forgedStatus = resp.StatusCode
		p.browser(t, codeFor)(raw)
	}
	tok, err := loginPKCE(context.Background(), p.config(t), open, 10*time.Second)
	require.NoError(t, err, "a forged redirect is refused and the real one still lands")
	assert.Equal(t, http.StatusBadRequest, forgedStatus)
	assert.Equal(t, "at", tok.AccessToken)
	assert.Equal(t, "the-code", p.tokenForm.Get("code"))
}

// The flow dispatch: an unknown flow is refused before anything is
// discovered or opened.
func TestSignInRefusesAnUnknownFlow(t *testing.T) {
	_, err := OAuthSettings{Issuer: "https://idm.invalid", ClientID: "rou2", Flow: "implicit"}.SignIn("band")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"implicit"`)
}

func TestLoginPKCEReportsTheIdPsRefusal(t *testing.T) {
	p := newPKCEIdP(t)
	refuse := func(state string) url.Values {
		return url.Values{"error": {"access_denied"}, "error_description": {"not in rou2_users"}, "state": {state}}
	}
	_, err := loginPKCE(context.Background(), p.config(t), p.browser(t, refuse), 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access_denied")
	assert.Contains(t, err.Error(), "not in rou2_users")
}

func TestLoginPKCEInsistsOnARefreshToken(t *testing.T) {
	p := newPKCEIdP(t)
	p.noRefresh = true
	_, err := loginPKCE(context.Background(), p.config(t), p.browser(t, codeFor), 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh_token")
}

func TestLoginPKCETimesOutWhenNobodyComesBack(t *testing.T) {
	p := newPKCEIdP(t)
	_, err := loginPKCE(context.Background(), p.config(t), func(string) {}, 150*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sign-in came back")
}

// An authorization endpoint off the issuer's origin would send the browser,
// and the person's password, to a host the issuer never named.
func TestPKCEConfigRefusesAnAuthorizeEndpointOffTheIssuer(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": "https://phish.example/ui/oauth2",
			"token_endpoint":         srv.URL + "/oauth2/token",
		})
	}))
	t.Cleanup(srv.Close)
	prev := oidcHTTP
	oidcHTTP = srv.Client()
	t.Cleanup(func() { oidcHTTP = prev })
	_, err := OAuthSettings{Issuer: srv.URL, ClientID: "rou2", Flow: FlowPKCE}.PKCEConfig("band")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not on the issuer")
}
