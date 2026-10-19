package auth

import (
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
	"os/exec"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

// FlowPKCE is the authorization code grant with PKCE (RFC 7636) and a loopback
// redirect (RFC 8252), for an IdP with no device flow. The client is public:
// the code verifier stands in for a secret.
const FlowPKCE = "pkce"

// pkceCallbackPath is the loopback redirect's path. The redirect names
// 127.0.0.1, not localhost (RFC 8252 section 7.3): a literal cannot resolve to
// ::1 while figaro listens on IPv4. The port is whatever the OS hands out, so
// the IdP must accept any loopback port (Kanidm: enable-localhost-redirects on a
// public client, measured to accept http://127.0.0.1:<any port><any path>).
const pkceCallbackPath = "/callback"

// pkceTimeout bounds how long a login waits for the browser to come back.
const pkceTimeout = 5 * time.Minute

type PKCEConfig struct {
	ProviderName string
	ClientID     string
	Scope        string
	AuthorizeURL string
	TokenURL     string
}

// PKCEConfig resolves the issuer's endpoints for the authorization code flow.
// Scopes are sent as the file names them: unlike the device flow, nothing is
// added, because offline_access is not universal (Kanidm issues refresh tokens
// without it).
func (s OAuthSettings) PKCEConfig(provider string) (PKCEConfig, error) {
	if s.Flow != FlowPKCE {
		return PKCEConfig{}, fmt.Errorf("%s: oauth flow %q is not %q", provider, s.Flow, FlowPKCE)
	}
	issuer := strings.TrimRight(strings.TrimSpace(s.Issuer), "/")
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" || iu.Host == "" {
		return PKCEConfig{}, fmt.Errorf("%s: oauth issuer %q is not an https URL", provider, s.Issuer)
	}
	if strings.TrimSpace(s.ClientID) == "" {
		return PKCEConfig{}, fmt.Errorf("%s: oauth names no client_id", provider)
	}
	disc := issuer + "/.well-known/openid-configuration"
	var oc oidcConfiguration
	if err := getJSON(disc, &oc); err != nil {
		return PKCEConfig{}, fmt.Errorf("%s: oidc discovery: %w", provider, err)
	}
	if strings.TrimRight(oc.Issuer, "/") != issuer {
		return PKCEConfig{}, fmt.Errorf("%s: %s names issuer %q, the provider file names %q", provider, disc, oc.Issuer, s.Issuer)
	}
	if oc.AuthorizationEndpoint == "" || oc.TokenEndpoint == "" {
		return PKCEConfig{}, fmt.Errorf("%s: %s offers no authorization code flow (no authorization or token endpoint)", provider, issuer)
	}
	for _, e := range []string{oc.AuthorizationEndpoint, oc.TokenEndpoint} {
		if !sameOrigin(e, iu) {
			return PKCEConfig{}, fmt.Errorf("%s: endpoint %q is not on the issuer %s", provider, e, issuer)
		}
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
	}, nil
}

// LoginPKCE signs a person in through their browser: it listens on a loopback
// port, sends the browser to the IdP with a PKCE challenge, takes the code the
// IdP redirects back with, and exchanges it. It insists on a refresh token,
// because a credential hush cannot refresh dies at its first expiry.
func LoginPKCE(cfg PKCEConfig) (DeviceTokens, error) {
	return loginPKCE(context.Background(), cfg, openInBrowser, pkceTimeout)
}

func loginPKCE(ctx context.Context, cfg PKCEConfig, open func(string), timeout time.Duration) (DeviceTokens, error) {
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

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	finish := func(r result) {
		select {
		case done <- r:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(pkceCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			// Not ours: a stale tab or a forged redirect. Keep waiting.
			http.Error(w, "this sign-in link is not the one figaro is waiting for", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			writeLoginPage(w, http.StatusBadRequest, "Sign-in failed: "+e+". You can close this tab.")
			finish(result{err: fmt.Errorf("%s: the IdP refused the sign-in: %s: %s", cfg.ProviderName, e, q.Get("error_description"))})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no authorization code", http.StatusBadRequest)
			return
		}
		writeLoginPage(w, http.StatusOK, "Signed in to "+cfg.ProviderName+". You can close this tab.")
		finish(result{code: code})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
	}
	if cfg.Scope != "" {
		q.Set("scope", cfg.Scope)
	}
	authorize := cfg.AuthorizeURL
	if strings.Contains(authorize, "?") {
		authorize += "&" + q.Encode()
	} else {
		authorize += "?" + q.Encode()
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim(cfg.ProviderName+" signs in through your browser (authorization code + PKCE)."))
	fmt.Fprintln(os.Stderr, "       If it does not open, visit:")
	fmt.Fprintln(os.Stderr, "       "+authorize)
	fmt.Fprintln(os.Stderr)
	open(authorize)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return DeviceTokens{}, fmt.Errorf("%s: no sign-in came back within %s", cfg.ProviderName, timeout)
	}
	if res.err != nil {
		return DeviceTokens{}, res.err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {cfg.ClientID},
		"code_verifier": {pkce.Verifier},
	}
	tr, err := requestToken(oidcHTTP, cfg.TokenURL, cfg.ClientID, form, nil)
	if err != nil {
		return DeviceTokens{}, fmt.Errorf("%s: %w", cfg.ProviderName, err)
	}
	if tr.RefreshToken == "" {
		return DeviceTokens{}, errors.New(cfg.ProviderName + ": the IdP issued no refresh_token, so hush could not keep this sign-in fresh; " +
			"check the client allows refresh, or that the scopes ask for it (some IdPs need offline_access)")
	}
	return DeviceTokens{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, ExpiresIn: tr.ExpiresIn}, nil
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64urlEncode(b), nil
}

func writeLoginPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><title>figaro</title><p>%s</p>", html.EscapeString(msg))
}

// openInBrowser honours $BROWSER before the platform opener: on a WSL or
// headless Linux box, xdg-open is often absent while $BROWSER is set.
func openInBrowser(u string) {
	if b := strings.TrimSpace(os.Getenv("BROWSER")); b != "" {
		if err := exec.Command(b, u).Start(); err == nil {
			return
		}
	}
	openBrowser(u)
}
