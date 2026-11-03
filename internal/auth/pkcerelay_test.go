package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRelay plays the code relay: it answers 404 for every state until the
// browser "lands" (deliver), then hands the stored answer over ONCE.
type fakeRelay struct {
	srv     *httptest.Server
	mu      sync.Mutex
	stored  map[string]map[string]string
	polls   atomic.Int32
	failFor atomic.Int32 // answer 503 to this many polls first
}

func newFakeRelay(t *testing.T) *fakeRelay {
	r := &fakeRelay{stored: map[string]map[string]string{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.polls.Add(1)
		if r.failFor.Load() > 0 {
			r.failFor.Add(-1)
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		st := req.URL.Query().Get("state")
		r.mu.Lock()
		ans, ok := r.stored[st]
		delete(r.stored, st)
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRelay) deliver(state string, ans map[string]string) {
	r.mu.Lock()
	r.stored[state] = ans
	r.mu.Unlock()
}

func (p *pkceIdP) relayConfig(t *testing.T, relay string) PKCEConfig {
	t.Helper()
	cfg, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		Scopes: []string{"openid"}, RedirectURI: pasteRedirect, CodeRelay: relay}.PKCEConfig("band")
	require.NoError(t, err)
	return cfg
}

// fastRelay makes the poll pauses test-sized for one test.
func fastRelay(t *testing.T) {
	gap, back := relayMinGap, relayBackoff
	relayMinGap, relayBackoff = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { relayMinGap, relayBackoff = gap, back })
}

// browserLandsAfter returns an open func that records the state and, after
// some 404 polls, has the "browser" deliver ans for it to the relay.
func (p *pkceIdP) browserLandsAfter(t *testing.T, r *fakeRelay, polls int32, ans func(state string) map[string]string) func(string) {
	return func(raw string) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		p.mu.Lock()
		p.authorize = u.Query()
		p.mu.Unlock()
		state := u.Query().Get("state")
		go func() {
			for r.polls.Load() < polls {
				time.Sleep(5 * time.Millisecond)
			}
			r.deliver(state, ans(state))
		}()
	}
}

func noPaste(string) (string, error) { return "", errors.New("stdin closed") }

func TestPKCEConfigCodeRelayNeedsARedirectAndTheSameTransportRule(t *testing.T) {
	p := newPKCEIdP(t)
	_, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		CodeRelay: "https://relay.example/code"}.PKCEConfig("band")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without redirect_uri")

	_, err = OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		RedirectURI: pasteRedirect, CodeRelay: "http://relay.example/code"}.PKCEConfig("band")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code_relay")

	cfg, err := OAuthSettings{Issuer: p.srv.URL, ClientID: "rou2", Flow: FlowPKCE,
		RedirectURI: pasteRedirect, CodeRelay: "https://relay.example/code"}.PKCEConfig("band")
	require.NoError(t, err)
	assert.Equal(t, "https://relay.example/code", cfg.CodeRelay)
}

// The order's control: the browser signs in elsewhere, figaro gets the code
// from the relay with no paste, and exchanges it with its own verifier.
func TestLoginPKCEPicksTheCodeUpFromTheRelayWithNoPaste(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	open := p.browserLandsAfter(t, r, 3, func(state string) map[string]string {
		return map[string]string{"state": state, "code": "the-code"}
	})
	tok, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL+"/figaro/code"), open, 10*time.Second, noPaste)
	require.NoError(t, err, "a closed stdin is not an answer while the relay can still bring one")
	assert.Equal(t, "at", tok.AccessToken)
	assert.Equal(t, "the-code", p.tokenForm.Get("code"))
	assert.Equal(t, pasteRedirect, p.tokenForm.Get("redirect_uri"))
	assert.NotEmpty(t, p.tokenForm.Get("code_verifier"), "the verifier comes from this process, never the relay")
	assert.GreaterOrEqual(t, r.polls.Load(), int32(3), "404s were waited out, not fatal")
}

func TestLoginPKCERelayRidesOutServerErrors(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	r.failFor.Store(3)
	open := p.browserLandsAfter(t, r, 1, func(state string) map[string]string {
		return map[string]string{"state": state, "code": "the-code"}
	})
	_, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL), open, 10*time.Second, noPaste)
	require.NoError(t, err)
	assert.Equal(t, "the-code", p.tokenForm.Get("code"))
}

func TestLoginPKCERelayReportsTheIdPsRefusal(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	open := p.browserLandsAfter(t, r, 1, func(state string) map[string]string {
		return map[string]string{"state": state, "error": "access_denied", "error_description": "not in the allowed group"}
	})
	_, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL), open, 10*time.Second, noPaste)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the allowed group")
	assert.Nil(t, p.tokenForm, "nothing was exchanged")
}

// A relay that answers for some other state is refused before the code is
// spent: it is either broken or not ours.
func TestLoginPKCERelayRefusesAnAnswerForAnotherState(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	open := p.browserLandsAfter(t, r, 1, func(state string) map[string]string {
		return map[string]string{"state": "not-ours", "code": "stolen"} // stored under OUR state, naming another
	})
	_, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL), open, 10*time.Second, noPaste)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different sign-in")
	assert.Nil(t, p.tokenForm)
}

// The paste path stays: if the user pastes first, the relay is not needed.
func TestLoginPKCEPasteStillWinsWhileTheRelayHasNothing(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	open, read, _ := p.pasteUser(t, func(state string) string {
		return redirectedURL(url.Values{"code": {"the-code"}, "state": {state}})
	})
	_, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL), open, 10*time.Second, read)
	require.NoError(t, err)
	assert.Equal(t, "the-code", p.tokenForm.Get("code"))
}

// A wrong-state paste still aborts the sign-in even with a relay configured.
func TestLoginPKCEWrongStatePasteAbortsEvenWithARelay(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	open, read, _ := p.pasteUser(t, func(string) string {
		return redirectedURL(url.Values{"code": {"x"}, "state": {"not-ours"}})
	})
	_, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL), open, 10*time.Second, read)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the one figaro is waiting for")
	assert.Nil(t, p.tokenForm)
}

func TestLoginPKCERelayTimesOutCleanly(t *testing.T) {
	fastRelay(t)
	p, r := newPKCEIdP(t), newFakeRelay(t)
	open := func(string) {}
	_, err := loginPKCE(context.Background(), p.relayConfig(t, r.srv.URL), open, 150*time.Millisecond, noPaste)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sign-in came back")
	assert.Nil(t, p.tokenForm)
}
