package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
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
	CodeRelay    string
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
	relay := strings.TrimSpace(s.CodeRelay)
	if relay != "" {
		if redirect == "" {
			return PKCEConfig{}, fmt.Errorf("%s: oauth code_relay is set without redirect_uri: a relay only sees codes sent to its own redirect", provider)
		}
		if err := checkCodeURL("code_relay", relay); err != nil {
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
		CodeRelay:    relay,
	}, nil
}

func checkRedirectURI(raw string) error { return checkCodeURL("redirect_uri", raw) }

// checkCodeURL holds every URL the authorization code travels through to the
// same rule: https, or plain http only on a loopback address.
func checkCodeURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s %q is not an absolute URL", key, raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("%s %q must be https (plain http only on a loopback address): the authorization code travels in it", key, raw)
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
	if read == nil && cfg.CodeRelay == "" {
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
	if cfg.CodeRelay != "" {
		fmt.Fprintln(os.Stderr, "       "+term.Dim("figaro picks the sign-in up by itself once the browser lands; pasting what the page shows also works."))
	} else {
		fmt.Fprintln(os.Stderr, "       "+term.Dim("The page it lands on shows the code. Bring it back here."))
	}
	fmt.Fprintln(os.Stderr)
	open(authorize)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	// The relay poll must not outlive the sign-in: cancel it and wait for it.
	// (The paste reader may sit on stdin forever; it shares nothing.)
	var relayDone sync.WaitGroup
	defer func() { cancel(); relayDone.Wait() }()

	// Two ways back, raced: the relay hands figaro the code it holds for this
	// state, and the user can paste what the page shows. The first answer wins.
	// A paste that cannot be read at all (no terminal, stdin closed) is not an
	// answer while a relay can still bring one.
	answers := make(chan pkceAnswer, 2)
	waiting := 0
	if cfg.CodeRelay != "" {
		waiting++
		relayDone.Add(1)
		go func() {
			defer relayDone.Done()
			answers <- pollRelay(ctx, cfg, state)
		}()
	}
	if read != nil {
		waiting++
		go func() { answers <- pastedAnswer(ctx, cfg, state, timeout, read) }()
	}
	var got pkceAnswer
	for {
		select {
		case got = <-answers:
		case <-ctx.Done():
			return DeviceTokens{}, fmt.Errorf("%s: no sign-in came back within %s", cfg.ProviderName, timeout)
		}
		waiting--
		if errors.Is(got.err, errPasteUnreadable) && waiting > 0 {
			continue
		}
		break
	}
	if got.err != nil {
		return DeviceTokens{}, got.err
	}
	if got.idpErr != "" {
		return DeviceTokens{}, fmt.Errorf("%s: the IdP refused the sign-in: %q: %q", cfg.ProviderName, got.idpErr, got.idpDesc)
	}
	return exchangePKCE(cfg, got.code, cfg.RedirectURI, pkce.Verifier)
}

// pkceAnswer is one way back from the browser: a code for figaro's state, the
// IdP's refusal, or why this way could not bring either.
type pkceAnswer struct {
	code, idpErr, idpDesc string
	err                   error
}

var errPasteUnreadable = errors.New("the pasted code could not be read")

func pastedAnswer(ctx context.Context, cfg PKCEConfig, state string, timeout time.Duration, read func(string) (string, error)) pkceAnswer {
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
			if l.err != nil {
				return "", fmt.Errorf("%w: %w", errPasteUnreadable, l.err)
			}
			return strings.TrimSpace(l.s), nil
		case <-ctx.Done():
			return "", fmt.Errorf("%s: no sign-in came back within %s", cfg.ProviderName, timeout)
		}
	}
	mismatch := fmt.Errorf("%s: the pasted sign-in is not the one figaro is waiting for (state mismatch); start again", cfg.ProviderName)

	pasted, err := ask("Paste the code shown on the page (or the full redirected URL): ")
	if err != nil {
		return pkceAnswer{err: err}
	}
	code, gotState, idpErr, idpDesc := parsePasted(pasted)
	if gotState != "" && subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
		return pkceAnswer{err: mismatch}
	}
	if idpErr != "" {
		return pkceAnswer{idpErr: idpErr, idpDesc: idpDesc}
	}
	if code == "" {
		return pkceAnswer{err: fmt.Errorf("%s: no authorization code in what was pasted", cfg.ProviderName)}
	}
	if gotState == "" {
		if gotState, err = ask("Paste the state shown on the page: "); err != nil {
			return pkceAnswer{err: err}
		}
		if subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
			return pkceAnswer{err: mismatch}
		}
	}
	return pkceAnswer{code: code}
}

// relayHTTP waits out a long-poll: the relay holds a request open while the
// browser has not landed yet, so the timeout is a ceiling, not an estimate.
var relayHTTP = &http.Client{Timeout: 60 * time.Second}

// Pauses between relay polls. A relay that answers "not yet" at once instead
// of holding the request is still polled no faster than relayMinGap.
var (
	relayMinGap  = time.Second
	relayBackoff = 2 * time.Second
)

// pollRelay asks the code relay, with figaro's own state, until it hands over
// the code the browser delivered or the context ends. The relay answers 404
// for every state it holds nothing for: not arrived yet, wrong, expired or
// already collected. figaro cannot tell those apart and does not need to: it
// keeps asking until its deadline. A code is useless without the PKCE
// verifier, which never leaves this process.
func pollRelay(ctx context.Context, cfg PKCEConfig, state string) pkceAnswer {
	u, err := url.Parse(cfg.CodeRelay)
	if err != nil {
		return pkceAnswer{err: fmt.Errorf("%s: code_relay: %w", cfg.ProviderName, err)}
	}
	q := u.Query()
	q.Set("state", state)
	u.RawQuery = q.Encode()
	target := u.String()

	pause := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for ctx.Err() == nil {
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return pkceAnswer{err: fmt.Errorf("%s: code_relay: %w", cfg.ProviderName, err)}
		}
		req.Header.Set("Accept", "application/json")
		resp, err := relayHTTP.Do(req)
		if err != nil {
			if !pause(relayBackoff) {
				break
			}
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			return relayAnswer(cfg, state, body)
		case resp.StatusCode == http.StatusNotFound:
			if wait := relayMinGap - time.Since(started); wait > 0 && !pause(wait) {
				return pkceAnswer{err: ctx.Err()}
			}
		default:
			if !pause(relayBackoff) {
				return pkceAnswer{err: ctx.Err()}
			}
		}
	}
	return pkceAnswer{err: ctx.Err()}
}

func relayAnswer(cfg PKCEConfig, state string, body []byte) pkceAnswer {
	var got struct {
		State            string `json:"state"`
		Code             string `json:"code"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return pkceAnswer{err: fmt.Errorf("%s: the code relay's answer is not JSON: %w", cfg.ProviderName, err)}
	}
	if subtle.ConstantTimeCompare([]byte(got.State), []byte(state)) != 1 {
		return pkceAnswer{err: fmt.Errorf("%s: the code relay answered for a different sign-in (state mismatch); start again", cfg.ProviderName)}
	}
	if got.Error != "" {
		return pkceAnswer{idpErr: got.Error, idpDesc: got.ErrorDescription}
	}
	if got.Code == "" {
		return pkceAnswer{err: fmt.Errorf("%s: the code relay answered with no authorization code", cfg.ProviderName)}
	}
	return pkceAnswer{code: got.Code}
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
