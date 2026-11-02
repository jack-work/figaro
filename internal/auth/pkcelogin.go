package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

const (
	FlowPKCE         = "pkce"
	pkceCallbackPath = "/callback"
	pkceTimeout      = 5 * time.Minute
)

type PKCEConfig struct {
	ProviderName string
	ClientID     string
	Scope        string
	AuthorizeURL string
	TokenURL     string
	RedirectURI  string
}

func (s OAuthSettings) PKCEConfig(provider string) (PKCEConfig, error) {
	if s.Flow != FlowPKCE {
		return PKCEConfig{}, fmt.Errorf("%s: oauth flow %q is not %q", provider, s.Flow, FlowPKCE)
	}
	if strings.TrimSpace(s.ClientID) == "" {
		return PKCEConfig{}, fmt.Errorf("%s: oauth names no client_id", provider)
	}
	redirect := strings.TrimSpace(s.RedirectURI)
	if redirect != "" {
		if err := checkRedirectURI(redirect); err != nil {
			return PKCEConfig{}, fmt.Errorf("%s: oauth %w", provider, err)
		}
	}
	oc, iu, err := discover(oidcHTTP, s.Issuer)
	if err != nil {
		return PKCEConfig{}, fmt.Errorf("%s: oauth %w", provider, err)
	}
	if oc.AuthorizationEndpoint == "" {
		return PKCEConfig{}, fmt.Errorf("%s: %s offers no authorization code flow (no authorization_endpoint)", provider, s.Issuer)
	}
	if !sameOrigin(oc.AuthorizationEndpoint, iu) {
		return PKCEConfig{}, fmt.Errorf("%s: authorization endpoint %q is not on the issuer %s", provider, oc.AuthorizationEndpoint, s.Issuer)
	}
	var scopes []string
	for _, sc := range s.Scopes {
		if sc = strings.TrimSpace(sc); sc != "" {
			scopes = append(scopes, sc)
		}
	}
	return PKCEConfig{
		ProviderName: provider,
		ClientID:     strings.TrimSpace(s.ClientID),
		Scope:        strings.Join(scopes, " "),
		AuthorizeURL: oc.AuthorizationEndpoint,
		TokenURL:     oc.TokenEndpoint,
		RedirectURI:  redirect,
	}, nil
}

func checkRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("redirect_uri %q is not an absolute URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("redirect_uri %q must be https (plain http only on a loopback address): the authorization code travels in it", raw)
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func LoginPKCE(cfg PKCEConfig) (DeviceTokens, error) {
	return loginPKCE(context.Background(), cfg, openBrowser, pkceTimeout, pasteLine)
}

type pkceRedirect struct {
	code string
	err  error
}

func loginPKCE(ctx context.Context, cfg PKCEConfig, open func(string), timeout time.Duration, read func(string) (string, error)) (DeviceTokens, error) {
	if cfg.RedirectURI != "" {
		return loginPKCEPasted(ctx, cfg, open, timeout, read)
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return DeviceTokens{}, err
	}
	state, err := randomState()
	if err != nil {
		return DeviceTokens{}, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return DeviceTokens{}, fmt.Errorf("%s: loopback listener: %w", cfg.ProviderName, err)
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, pkceCallbackPath)
	done := make(chan pkceRedirect, 1)
	srv := &http.Server{Handler: pkceCallback(cfg.ProviderName, state, done), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	authorize := authorizeURL(cfg, redirect, state, pkce.Challenge)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim(cfg.ProviderName+" signs in through your browser (authorization code with PKCE). If it does not open, visit:"))
	fmt.Fprintln(os.Stderr, "       "+term.Cyan(authorize))
	fmt.Fprintln(os.Stderr)
	open(authorize)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var got pkceRedirect
	select {
	case got = <-done:
	case <-ctx.Done():
		return DeviceTokens{}, fmt.Errorf("%s: no sign-in came back within %s", cfg.ProviderName, timeout)
	}
	if got.err != nil {
		return DeviceTokens{}, got.err
	}
	return exchangePKCE(cfg, got.code, redirect, pkce.Verifier)
}

func exchangePKCE(cfg PKCEConfig, code, redirect, verifier string) (DeviceTokens, error) {
	tr, err := requestToken(oidcHTTP, cfg.TokenURL, cfg.ClientID, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}, nil)
	if err != nil {
		return DeviceTokens{}, fmt.Errorf("%s: %w", cfg.ProviderName, err)
	}
	if tr.RefreshToken == "" {
		return DeviceTokens{}, errors.New(cfg.ProviderName + ": the IdP issued no refresh_token, so hush could not keep this sign-in fresh; " +
			"check that the client may refresh, or that its scopes ask for it (some IdPs want offline_access)")
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized by "+cfg.AuthorizeURL)
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
	sep := "?"
	if strings.Contains(cfg.AuthorizeURL, "?") {
		sep = "&"
	}
	return cfg.AuthorizeURL + sep + q.Encode()
}

func pkceCallback(provider, state string, done chan<- pkceRedirect) http.Handler {
	finish := func(r pkceRedirect) {
		select {
		case done <- r:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(pkceCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			loginPage(w, http.StatusBadRequest, "This sign-in link is not the one figaro is waiting for.")
			return
		}
		if e := q.Get("error"); e != "" {
			loginPage(w, http.StatusBadRequest, "Sign-in failed: "+e+". You can close this tab.")
			finish(pkceRedirect{err: fmt.Errorf("%s: the IdP refused the sign-in: %q: %q", provider, e, q.Get("error_description"))})
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
	return mux
}

func loginPKCEPasted(ctx context.Context, cfg PKCEConfig, open func(string), timeout time.Duration, read func(string) (string, error)) (DeviceTokens, error) {
	if read == nil {
		return DeviceTokens{}, fmt.Errorf("%s: redirect_uri is set but nothing can read the pasted code", cfg.ProviderName)
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return DeviceTokens{}, err
	}
	state, err := randomState()
	if err != nil {
		return DeviceTokens{}, err
	}
	authorize := authorizeURL(cfg, cfg.RedirectURI, state, pkce.Challenge)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim(cfg.ProviderName+" signs in through a browser, on any machine (authorization code with PKCE). Open:"))
	fmt.Fprintln(os.Stderr, "       "+term.Cyan(authorize))
	fmt.Fprintln(os.Stderr, "       "+term.Dim("The page it lands on shows the code. Bring it back here."))
	fmt.Fprintln(os.Stderr)
	open(authorize)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ask := func(prompt string) (string, error) {
		type line struct {
			s   string
			err error
		}
		got := make(chan line, 1)
		go func() {
			s, err := read(prompt)
			got <- line{s, err}
		}()
		select {
		case l := <-got:
			return strings.TrimSpace(l.s), l.err
		case <-ctx.Done():
			return "", fmt.Errorf("%s: no sign-in came back within %s", cfg.ProviderName, timeout)
		}
	}

	pasted, err := ask("Paste the code shown on the page (or the full redirected URL): ")
	if err != nil {
		return DeviceTokens{}, err
	}
	code, gotState, idpErr, idpDesc := parsePasted(pasted)
	if gotState != "" && subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
		return DeviceTokens{}, fmt.Errorf("%s: the pasted sign-in is not the one figaro is waiting for (state mismatch); start again", cfg.ProviderName)
	}
	if idpErr != "" {
		return DeviceTokens{}, fmt.Errorf("%s: the IdP refused the sign-in: %q: %q", cfg.ProviderName, idpErr, idpDesc)
	}
	if code == "" {
		return DeviceTokens{}, fmt.Errorf("%s: no authorization code in what was pasted", cfg.ProviderName)
	}
	if gotState == "" {
		if gotState, err = ask("Paste the state shown on the page: "); err != nil {
			return DeviceTokens{}, err
		}
		if subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
			return DeviceTokens{}, fmt.Errorf("%s: the pasted sign-in is not the one figaro is waiting for (state mismatch); start again", cfg.ProviderName)
		}
	}
	return exchangePKCE(cfg, code, cfg.RedirectURI, pkce.Verifier)
}

func parsePasted(in string) (code, state, idpErr, idpDesc string) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", "", "", ""
	}
	var q url.Values
	switch {
	case strings.Contains(in, "://"):
		u, err := url.Parse(in)
		if err != nil {
			return "", "", "", ""
		}
		q = u.Query()
	case strings.HasPrefix(in, "?") || strings.Contains(in, "code=") || strings.Contains(in, "error="):
		var err error
		if q, err = url.ParseQuery(strings.TrimPrefix(in, "?")); err != nil {
			return "", "", "", ""
		}
	default:
		return in, "", "", ""
	}
	return q.Get("code"), q.Get("state"), q.Get("error"), q.Get("error_description")
}

var stdin = bufio.NewReader(os.Stdin)

func pasteLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, "       "+prompt)
	s, err := stdin.ReadString('\n')
	if err != nil && s == "" {
		return "", fmt.Errorf("read the pasted code: %w", err)
	}
	return s, nil
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

func (s OAuthSettings) SignIn(provider string) (OIDCSignIn, error) {
	switch s.Flow {
	case "", FlowDevice:
		cfg, err := s.DeviceConfig(provider)
		if err != nil {
			return OIDCSignIn{}, err
		}
		tok, err := LoginOIDCDevice(cfg, s.Issuer)
		return OIDCSignIn{TokenURL: cfg.TokenURL, ClientID: cfg.ClientID, Scope: cfg.Scope, Tokens: tok}, err
	case FlowPKCE:
		cfg, err := s.PKCEConfig(provider)
		if err != nil {
			return OIDCSignIn{}, err
		}
		tok, err := LoginPKCE(cfg)
		return OIDCSignIn{TokenURL: cfg.TokenURL, ClientID: cfg.ClientID, Scope: cfg.Scope, Tokens: tok}, err
	}
	return OIDCSignIn{}, fmt.Errorf("%s: oauth flow %q is not %q or %q", provider, s.Flow, FlowDevice, FlowPKCE)
}
