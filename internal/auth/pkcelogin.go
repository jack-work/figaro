package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	FlowPKCE         = "pkce"
	pkceCallbackPath = "/callback"
	pkceTimeout      = 5 * time.Minute
)

type PKCEConfig struct {
	ProviderName    string
	ClientID        string
	Scope           string
	AuthorizeURL    string
	TokenURL        string
	RedirectURI     string
	AuthorizeParams map[string]string
}

func (s OAuthSettings) PKCEConfig(provider string) (PKCEConfig, error) {
	if s.Flow != FlowPKCE {
		return PKCEConfig{}, fmt.Errorf("%s: oauth flow %q is not %q", provider, s.Flow, FlowPKCE)
	}
	if strings.TrimSpace(s.ClientID) == "" {
		return PKCEConfig{}, fmt.Errorf("%s: oauth names no client_id", provider)
	}
	oc, iu, err := s.endpoints()
	if err != nil {
		return PKCEConfig{}, fmt.Errorf("%s: oauth %w", provider, err)
	}
	if oc.AuthorizationEndpoint == "" {
		return PKCEConfig{}, fmt.Errorf("%s: %s offers no authorization code flow (no authorization_endpoint)", provider, s.Issuer)
	}
	if err := validateOAuthEndpoint(oc.AuthorizationEndpoint, iu, true); err != nil {
		return PKCEConfig{}, fmt.Errorf("%s: authorization endpoint: %w", provider, err)
	}
	if err := (PKCEConfig{RedirectURI: s.RedirectURI, AuthorizeURL: oc.AuthorizationEndpoint, AuthorizeParams: s.AuthorizeParams}).validate(); err != nil {
		return PKCEConfig{}, fmt.Errorf("%s: %w", provider, err)
	}
	var scopes []string
	for _, sc := range s.Scopes {
		if sc = strings.TrimSpace(sc); sc != "" {
			scopes = append(scopes, sc)
		}
	}
	return PKCEConfig{
		ProviderName:    provider,
		ClientID:        strings.TrimSpace(s.ClientID),
		Scope:           strings.Join(scopes, " "),
		AuthorizeURL:    oc.AuthorizationEndpoint,
		TokenURL:        oc.TokenEndpoint,
		RedirectURI:     s.RedirectURI,
		AuthorizeParams: maps.Clone(s.AuthorizeParams),
	}, nil
}

func LoginPKCE(ctx context.Context, cfg PKCEConfig, c Console) (DeviceTokens, error) {
	return loginPKCE(ctx, cfg, func(u string) { c.Open(u) }, pkceTimeout, c)
}

type pkceRedirect struct {
	code string
	err  error
}

func loginPKCE(ctx context.Context, cfg PKCEConfig, open func(string), timeout time.Duration, c Console) (DeviceTokens, error) {
	if err := cfg.validate(); err != nil {
		return DeviceTokens{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return DeviceTokens{}, err
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return DeviceTokens{}, err
	}
	state, err := randomState()
	if err != nil {
		return DeviceTokens{}, err
	}
	ln, redirect, err := pkceListener(cfg.RedirectURI)
	if err != nil {
		return DeviceTokens{}, fmt.Errorf("%s: loopback listener: %w", cfg.ProviderName, err)
	}
	defer ln.Close()
	back, _ := url.Parse(redirect)
	done := make(chan pkceRedirect, 1)
	srv := &http.Server{Handler: pkceCallback(cfg.ProviderName, state, back, done), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()
	stop := context.AfterFunc(ctx, func() { srv.Close() })
	defer stop()

	authorize := authorizeURL(cfg, redirect, state, pkce.Challenge)
	c.Note(cfg.ProviderName + " signs in through your browser (authorization code with PKCE) at this link:")
	c.Link(authorize)
	open(authorize)

	var got pkceRedirect
	select {
	case got = <-done:
	case <-ctx.Done():
		if ctx.Err() == context.Canceled {
			return DeviceTokens{}, ctx.Err()
		}
		return DeviceTokens{}, fmt.Errorf("%s: no sign-in came back within %s", cfg.ProviderName, timeout)
	}
	if got.err != nil {
		return DeviceTokens{}, got.err
	}
	tr, err := requestTokenContext(ctx, oauthClient(), cfg.TokenURL, cfg.ClientID, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {got.code},
		"redirect_uri":  {redirect},
		"client_id":     {cfg.ClientID},
		"code_verifier": {pkce.Verifier},
	}, nil)
	if err != nil {
		return DeviceTokens{}, fmt.Errorf("%s: %w", cfg.ProviderName, err)
	}
	if tr.RefreshToken == "" {
		return DeviceTokens{}, errors.New(cfg.ProviderName + ": the IdP issued no refresh_token, so hush could not keep this sign-in fresh; " +
			"check that the client may refresh, or that its scopes ask for it (some IdPs want offline_access)")
	}
	c.Done("Authorized by " + cfg.AuthorizeURL)
	return DeviceTokens{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, ExpiresIn: tr.ExpiresIn}, nil
}

func authorizeURL(cfg PKCEConfig, redirect, state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if cfg.Scope != "" {
		q.Set("scope", cfg.Scope)
	}
	u, _ := url.Parse(cfg.AuthorizeURL)
	for key, values := range u.Query() {
		q[key] = values
	}
	for key, value := range cfg.AuthorizeParams {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func pkceCallback(provider, state string, redirect *url.URL, done chan<- pkceRedirect) http.Handler {
	finish := func(r pkceRedirect) {
		select {
		case done <- r:
		default:
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Host != redirect.Host || r.URL.EscapedPath() != redirect.EscapedPath() {
			http.NotFound(w, r)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q["state"]) != 1 || len(q["code"]) > 1 || len(q["error"]) > 1 {
			loginPage(w, http.StatusBadRequest, "The sign-in callback has invalid or duplicate parameters.")
			return
		}
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			loginPage(w, http.StatusBadRequest, "This sign-in link is not the one figaro is waiting for.")
			return
		}
		if q.Get("error") != "" {
			loginPage(w, http.StatusBadRequest, "Sign-in was refused. You can close this tab.")
			finish(pkceRedirect{err: fmt.Errorf("%s: the IdP refused the sign-in", provider)})
			return
		}
		code := q.Get("code")
		if code == "" {
			loginPage(w, http.StatusBadRequest, "The IdP sent no authorization code.")
			return
		}
		loginPage(w, http.StatusOK, "figaro has the sign-in for "+provider+". You can close this tab and return to the terminal.")
		finish(pkceRedirect{code: code})
	})
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64urlEncode(b), nil
}

func loginPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><title>figaro</title><p>%s</p>", html.EscapeString(msg))
}

type OIDCSignIn struct {
	TokenURL string
	ClientID string
	Scope    string
	Tokens   DeviceTokens
}

func (s OAuthSettings) SignIn(ctx context.Context, provider string, c Console) (OIDCSignIn, error) {
	switch s.Flow {
	case "", FlowDevice:
		cfg, err := s.DeviceConfig(provider)
		if err != nil {
			return OIDCSignIn{}, err
		}
		tok, err := LoginOIDCDevice(ctx, cfg, s.Issuer, c)
		return OIDCSignIn{TokenURL: cfg.TokenURL, ClientID: cfg.ClientID, Scope: cfg.Scope, Tokens: tok}, err
	case FlowPKCE:
		cfg, err := s.PKCEConfig(provider)
		if err != nil {
			return OIDCSignIn{}, err
		}
		tok, err := LoginPKCE(ctx, cfg, c)
		return OIDCSignIn{TokenURL: cfg.TokenURL, ClientID: cfg.ClientID, Scope: cfg.Scope, Tokens: tok}, err
	}
	return OIDCSignIn{}, fmt.Errorf("%s: oauth flow %q is not %q or %q", provider, s.Flow, FlowDevice, FlowPKCE)
}
