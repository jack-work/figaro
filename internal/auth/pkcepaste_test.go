package auth

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pasteRedirect = "https://login.example/callback"

func (p *pkceIdP) pasteConfig(t *testing.T) PKCEConfig {
	t.Helper()
	cfg, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		Scopes: []string{"openid", "groups", "profile"}, RedirectURI: pasteRedirect}.PKCEConfig("band")
	require.NoError(t, err)
	return cfg
}

// pasteUser plays the user at a terminal on one machine and a browser on
// another: open only records the authorize URL (nothing listens for it), and
// each prompt is answered by the next line, built from that URL's state.
func (p *pkceIdP) pasteUser(t *testing.T, lines ...func(state string) string) (func(string), func(string) (string, error), *[]string) {
	var state string
	var prompts []string
	open := func(raw string) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		p.mu.Lock()
		p.authorize = u.Query()
		p.mu.Unlock()
		state = u.Query().Get("state")
	}
	read := func(prompt string) (string, error) {
		prompts = append(prompts, prompt)
		if len(lines) == 0 {
			return "", errors.New("no more input")
		}
		next := lines[0]
		lines = lines[1:]
		return next(state), nil
	}
	return open, read, &prompts
}

func redirectedURL(q url.Values) string { return pasteRedirect + "?" + q.Encode() }

func TestPKCEConfigCarriesTheRedirectURI(t *testing.T) {
	p := newPKCEIdP(t)
	assert.Equal(t, pasteRedirect, p.pasteConfig(t).RedirectURI)
	assert.Empty(t, p.config(t).RedirectURI, "unset stays unset: the loopback flow")
}

func TestPKCEConfigRefusesARedirectThatWouldCarryTheCodeInPlaintext(t *testing.T) {
	p := newPKCEIdP(t)
	for _, bad := range []string{"http://login.example/callback", "login.example/callback", "/callback"} {
		_, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE, RedirectURI: bad}.PKCEConfig("band")
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "redirect_uri", bad)
	}
	_, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		RedirectURI: "http://127.0.0.1:8400/callback"}.PKCEConfig("band")
	require.NoError(t, err, "plain http stays allowed on a loopback literal")
}

func TestLoginPKCEWithARedirectURIExchangesThePastedURL(t *testing.T) {
	p := newPKCEIdP(t)
	open, read, prompts := p.pasteUser(t, func(state string) string {
		return redirectedURL(url.Values{"code": {"the-code"}, "state": {state}})
	})
	tok, err := loginPKCE(context.Background(), p.pasteConfig(t), open, 10*time.Second, read)
	require.NoError(t, err)
	assert.Equal(t, DeviceTokens{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 900}, tok)

	assert.Equal(t, pasteRedirect, p.authorize.Get("redirect_uri"), "the authorize URL names the configured endpoint, no loopback")
	assert.Equal(t, pasteRedirect, p.tokenForm.Get("redirect_uri"), "the exchange repeats the redirect it authorized")
	assert.Equal(t, "the-code", p.tokenForm.Get("code"))
	assert.Len(t, *prompts, 1, "a full URL carries the state: one prompt")
}

func TestLoginPKCEWithARedirectURIAsksForTheStateAfterABareCode(t *testing.T) {
	p := newPKCEIdP(t)
	open, read, prompts := p.pasteUser(t,
		func(string) string { return "  the-code \n" },
		func(state string) string { return state },
	)
	tok, err := loginPKCE(context.Background(), p.pasteConfig(t), open, 10*time.Second, read)
	require.NoError(t, err)
	assert.Equal(t, "at", tok.AccessToken)
	require.Len(t, *prompts, 2)
	assert.Contains(t, (*prompts)[1], "state")
}

// The order's own test: a pasted sign-in whose state is not figaro's is
// refused before any code reaches the token endpoint.
func TestLoginPKCEWithARedirectURIRefusesAPasteWithTheWrongState(t *testing.T) {
	p := newPKCEIdP(t)
	open, read, _ := p.pasteUser(t, func(string) string {
		return redirectedURL(url.Values{"code": {"the-code"}, "state": {"not-ours"}})
	})
	_, err := loginPKCE(context.Background(), p.pasteConfig(t), open, 10*time.Second, read)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the one figaro is waiting for")
	assert.Nil(t, p.tokenForm, "nothing was exchanged")
}

func TestLoginPKCEWithARedirectURIReportsTheIdPsRefusal(t *testing.T) {
	p := newPKCEIdP(t)
	open, read, _ := p.pasteUser(t, func(state string) string {
		return redirectedURL(url.Values{"error": {"access_denied"}, "error_description": {"not in the allowed group"}, "state": {state}})
	})
	_, err := loginPKCE(context.Background(), p.pasteConfig(t), open, 10*time.Second, read)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access_denied")
	assert.Contains(t, err.Error(), "not in the allowed group")
	assert.Nil(t, p.tokenForm)
}

func TestLoginPKCEWithARedirectURIRefusesAnEmptyPaste(t *testing.T) {
	p := newPKCEIdP(t)
	open, read, _ := p.pasteUser(t, func(string) string { return "   " })
	_, err := loginPKCE(context.Background(), p.pasteConfig(t), open, 10*time.Second, read)
	require.Error(t, err)
	assert.Nil(t, p.tokenForm)
}
