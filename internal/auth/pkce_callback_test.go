package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func availableCallback(t *testing.T, host string) string {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return "http://" + net.JoinHostPort(host, fmt.Sprint(port)) + "/auth/callback"
}

func TestPKCEConfiguredCallbackCarriesExtrasAndExactURI(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost"} {
		t.Run(host, func(t *testing.T) {
			p := newPKCEIdP(t)
			cfg := p.config(t)
			cfg.RedirectURI = availableCallback(t, host)
			cfg.AuthorizeURL = p.srv.URL + "/custom/authorize?prompt=login"
			cfg.TokenURL = p.srv.URL + "/custom/token"
			cfg.AuthorizeParams = map[string]string{"originator": "generic-cli", "organization_prompt": "true"}
			_, err := loginPKCE(context.Background(), cfg, p.browser(t, codeFor), 3*time.Second, quiet{})
			require.NoError(t, err)
			require.Equal(t, cfg.RedirectURI, p.authorize.Get("redirect_uri"))
			require.Equal(t, cfg.RedirectURI, p.tokenForm.Get("redirect_uri"))
			require.Equal(t, "generic-cli", p.authorize.Get("originator"))
			require.Equal(t, "true", p.authorize.Get("organization_prompt"))
			require.Equal(t, "login", p.authorize.Get("prompt"))
			require.Len(t, p.authorize["state"], 1)
		})
	}
}

func TestPKCEConfiguredCallbackRejectsUnsafeURIs(t *testing.T) {
	for _, raw := range []string{"https://localhost:1455/callback", "http://example.invalid:1455/callback", "http://localhost/callback", "http://localhost:0/callback", "http://localhost:65536/callback", "http://user@localhost:1455/callback", "http://localhost:1455/callback?x=y", "http://localhost:1455/callback#code", "http://localhost:1455", "http://localhost:1455/callback?"} {
		t.Run(raw, func(t *testing.T) {
			_, err := callbackURL(raw)
			require.Error(t, err)
		})
	}
}

func TestPKCEOccupiedCallbackFailsBeforeOpeningBrowser(t *testing.T) {
	p := newPKCEIdP(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	cfg := p.config(t)
	cfg.RedirectURI = "http://" + ln.Addr().String() + "/auth/callback"
	opened := false
	_, err = loginPKCE(context.Background(), cfg, func(string) { opened = true }, time.Second, quiet{})
	require.ErrorContains(t, err, "loopback listener")
	require.False(t, opened)
}

func TestPKCECancellationClosesConfiguredListener(t *testing.T) {
	p := newPKCEIdP(t)
	cfg := p.config(t)
	cfg.RedirectURI = availableCallback(t, "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	_, err := loginPKCE(ctx, cfg, func(string) { cancel() }, time.Second, quiet{})
	require.ErrorIs(t, err, context.Canceled)
	u, err := url.Parse(cfg.RedirectURI)
	require.NoError(t, err)
	ln, err := net.Listen("tcp4", u.Host)
	require.NoError(t, err)
	ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Empty(t, p.tokenForm)
}

func TestPKCECallbackRejectsWrongStateHostAndPathBeforeCodeExchange(t *testing.T) {
	p := newPKCEIdP(t)
	cfg := p.config(t)
	cfg.RedirectURI = availableCallback(t, "127.0.0.1")
	open := func(raw string) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
		for _, mutate := range []func(*http.Request){
			func(r *http.Request) {
				r.URL.RawQuery = url.Values{"state": {"wrong"}, "code": {"unspent-code"}}.Encode()
			},
			func(r *http.Request) { r.URL.Path = "/wrong" },
			func(r *http.Request) { r.Host = "attacker.invalid" },
		} {
			back := *redirect
			back.RawQuery = codeFor(u.Query().Get("state")).Encode()
			req, err := http.NewRequest(http.MethodGet, back.String(), nil)
			require.NoError(t, err)
			mutate(req)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
			require.GreaterOrEqual(t, resp.StatusCode, 400)
			p.mu.Lock()
			require.Empty(t, p.tokenForm)
			p.mu.Unlock()
		}
		p.browser(t, codeFor)(raw)
	}
	_, err := loginPKCE(context.Background(), cfg, open, 3*time.Second, quiet{})
	require.NoError(t, err)
	require.Equal(t, "the-code", p.tokenForm.Get("code"))
}

func TestPKCERejectsDuplicateAuthorizationParameters(t *testing.T) {
	p := newPKCEIdP(t)
	for _, params := range []map[string]string{{"prompt": "other"}, {"Prompt": "other"}, {"x": "one", "X": "two"}, {" state ": "wrong"}} {
		cfg := p.config(t)
		cfg.AuthorizeURL += "?prompt=login"
		cfg.AuthorizeParams = params
		opened := false
		_, err := loginPKCE(context.Background(), cfg, func(string) { opened = true }, time.Second, quiet{})
		require.Error(t, err)
		require.False(t, opened)
	}
}

func TestPKCECanceledTokenExchangeDoesNotOutliveLogin(t *testing.T) {
	p := newPKCEIdP(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	old := p.srv.Config.Handler
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		old.ServeHTTP(w, r)
	})
	cfg := p.config(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := loginPKCE(ctx, cfg, p.browser(t, codeFor), 3*time.Second, quiet{}); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("token exchange ignored parent cancellation")
	}
}
