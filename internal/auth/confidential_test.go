package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	hush "github.com/jack-work/hush/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeSealingHush(t *testing.T) *hush.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "hush.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req struct {
				Op     string            `json:"op"`
				Values map[string]string `json:"values"`
			}
			_ = json.NewDecoder(conn).Decode(&req)
			switch req.Op {
			case "encrypt":
				out := make(map[string]string, len(req.Values))
				for k, v := range req.Values {
					if strings.HasPrefix(v, "AGE-ENC[") {
						out[k] = v
					} else {
						out[k] = "AGE-ENC[" + base64.StdEncoding.EncodeToString([]byte(v)) + "]"
					}
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "values": out})
			case "decrypt":
				out := make(map[string]string, len(req.Values))
				for k, v := range req.Values {
					if body, ok := strings.CutPrefix(strings.TrimSuffix(v, "]"), "AGE-ENC["); ok {
						raw, _ := base64.StdEncoding.DecodeString(body)
						out[k] = string(raw)
					} else {
						out[k] = v
					}
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "values": out})
			case "oauth_delete":
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": false, "error": "not found", "error_code": "oauth_not_found"})
			default:
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true})
			}
			conn.Close()
		}
	}()
	return hush.NewWithSocket(sock)
}

type askConsole struct {
	quiet
	mu     sync.Mutex
	link   string
	answer func(link string) string
}

func (a *askConsole) Link(u string) {
	a.mu.Lock()
	a.link = u
	a.mu.Unlock()
}

func (a *askConsole) Open(string) bool { return false }

func (a *askConsole) Ask(_ context.Context, _ string) (string, error) {
	a.mu.Lock()
	link := a.link
	a.mu.Unlock()
	return a.answer(link), nil
}

func TestPKCEConfigRefusesADiscoveredTokenEndpointOffTheIssuer(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/ui/oauth2",
			"token_endpoint":         "https://evil.example/oauth2/token",
		})
	}))
	t.Cleanup(srv.Close)
	prev := oidcHTTP
	oidcHTTP = srv.Client()
	t.Cleanup(func() { oidcHTTP = prev })

	_, err := OAuthSettings{Issuer: srv.URL, ClientID: "c", Flow: FlowPKCE}.PKCEConfig("band")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not on the issuer")
}

func TestCrossOriginConfidentialPKCEAndHushSealedRefresh(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("FIGARO_STATE_DIR", stateDir)

	secretPath := filepath.Join(t.TempDir(), "google-client-secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("  top-secret-value\n"), 0o600))

	var (
		mu          sync.Mutex
		challenge   string
		gotSecret   string
		refreshHits int
		rotate      bool
		rejectNext  bool
	)

	var issuerSrv *httptest.Server
	issuerSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(issuerSrv.Close)

	tokenSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		mu.Lock()
		defer mu.Unlock()
		gotSecret = r.PostForm.Get("client_secret")
		if gotSecret != "top-secret-value" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if base64urlEncode(sum[:]) != challenge || r.PostForm.Get("code") != "google-code" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-initial",
				"refresh_token": "rt-unrotated",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "refresh_token":
			refreshHits++
			if rejectNext {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			rt := r.PostForm.Get("refresh_token")
			if rt != "rt-unrotated" && rt != "rt-rotated" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			out := map[string]any{
				"access_token": "at-refreshed-" + string(rune('0'+refreshHits)),
				"token_type":   "Bearer",
				"expires_in":   3600,
			}
			if rotate {
				out["refresh_token"] = "rt-rotated"
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tokenSrv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(issuerSrv.Certificate())
	pool.AddCert(tokenSrv.Certificate())
	prev := oidcHTTP
	oidcHTTP = &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	t.Cleanup(func() { oidcHTTP = prev })

	settings := OAuthSettings{
		Issuer:           issuerSrv.URL,
		AuthorizeURL:     issuerSrv.URL + "/o/oauth2/v2/auth",
		TokenURL:         tokenSrv.URL + "/token",
		ClientID:         "google-desktop-client",
		ClientSecretFile: secretPath,
		Flow:             FlowPKCE,
		Scopes:           []string{"openid", "https://www.googleapis.com/auth/cloud-platform"},
		AuthorizeParams:  map[string]string{"access_type": "offline", "prompt": "consent"},
	}

	cons := &askConsole{answer: func(link string) string {
		u, err := url.Parse(link)
		require.NoError(t, err)
		mu.Lock()
		challenge = u.Query().Get("code_challenge")
		mu.Unlock()
		cb := u.Query().Get("redirect_uri") + "?code=google-code&state=" + url.QueryEscape(u.Query().Get("state"))
		return cb
	}}

	in, err := settings.SignIn(context.Background(), "gemini-gw", cons)
	require.NoError(t, err)
	assert.Equal(t, "at-initial", in.Tokens.AccessToken)
	assert.Equal(t, "rt-unrotated", in.Tokens.RefreshToken)
	assert.Equal(t, secretPath, in.ClientSecretFile)

	h := fakeSealingHush(t)
	require.NoError(t, RegisterOAuthIn(h, stateDir, "gemini-gw", in))

	rawOnDisk, err := os.ReadFile(filepath.Join(stateDir, "oauth", "gemini-gw.toml"))
	require.NoError(t, err)
	assert.NotContains(t, string(rawOnDisk), "at-initial", "access token must be AGE-sealed on disk")
	assert.NotContains(t, string(rawOnDisk), "rt-unrotated", "refresh token must be AGE-sealed on disk")
	assert.NotContains(t, string(rawOnDisk), "top-secret-value", "client secret must never be copied into state")

	listed, err := ConfidentialOAuthList()
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-gw"}, listed)

	o := &OAuth{Hush: h, Name: "gemini-gw", StateDir: stateDir}
	tok, ok, err := o.TryResolve()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "at-initial", tok)

	// Refresh when IdP omits refresh_token (Google's standard behavior): must preserve rt-unrotated.
	require.NoError(t, o.Invalidate("at-initial"))
	tok, err = o.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "at-refreshed-1", tok)

	// Second refresh proves rt-unrotated was preserved across the first refresh, and rotates to rt-rotated.
	mu.Lock()
	rotate = true
	mu.Unlock()
	require.NoError(t, o.Invalidate("at-refreshed-1"))
	tok, err = o.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "at-refreshed-2", tok)

	// Permanent 4xx marks grant dead with LoginRequiredError.
	mu.Lock()
	rejectNext = true
	mu.Unlock()
	err = o.Invalidate("at-refreshed-2")
	require.ErrorIs(t, err, ErrLoginRequired)
	assert.Contains(t, err.Error(), "figaro provider login gemini-gw")

	removed, err := ConfidentialOAuthRemove("gemini-gw")
	require.NoError(t, err)
	assert.True(t, removed)
	listed, err = ConfidentialOAuthList()
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestParsePastedPKCEValidatesStateAndExtractsCode(t *testing.T) {
	got := parsePastedPKCE("google", "expected-state", "http://127.0.0.1:43210/callback?code=abc123&state=expected-state")
	require.NoError(t, got.err)
	assert.Equal(t, "abc123", got.code)

	mismatch := parsePastedPKCE("google", "expected-state", "http://127.0.0.1:43210/callback?code=abc123&state=wrong")
	require.Error(t, mismatch.err)
	assert.Contains(t, mismatch.err.Error(), "state does not match")

	missingState := parsePastedPKCE("google", "expected-state", "http://127.0.0.1:43210/callback?code=abc123")
	require.Error(t, missingState.err)
	assert.Contains(t, missingState.err.Error(), "state does not match")

	bare := parsePastedPKCE("google", "expected-state", "  raw-auth-code#frame ")
	require.NoError(t, bare.err)
	assert.Equal(t, "raw-auth-code", bare.code)
}

func TestConfidentialOAuthEnforcesHTTPSAndCredentialNames(t *testing.T) {
	stateDir := t.TempDir()
	h := fakeSealingHush(t)
	secretPath := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("s3cr3t"), 0o600))

	err := RegisterOAuthIn(h, stateDir, "../escape", OIDCSignIn{
		TokenURL:         "https://oauth2.googleapis.com/token",
		ClientID:         "cid",
		ClientSecretFile: secretPath,
		Tokens:           DeviceTokens{AccessToken: "a", RefreshToken: "r", ExpiresIn: 60},
	})
	require.ErrorContains(t, err, "invalid oauth credential name")

	err = RegisterOAuthIn(h, stateDir, "plain-http", OIDCSignIn{
		TokenURL:         "http://oauth2.example/token",
		ClientID:         "cid",
		ClientSecretFile: secretPath,
		Tokens:           DeviceTokens{AccessToken: "a", RefreshToken: "r", ExpiresIn: 60},
	})
	require.ErrorContains(t, err, "https")

	_, _, err = mintRefreshWithSecret(context.Background(), oauthClient(), "http://oauth2.example/token", "cid", "s3cr3t", "rt")
	require.ErrorContains(t, err, "https")
}
