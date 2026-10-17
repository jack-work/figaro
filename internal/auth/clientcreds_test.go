package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ccSecret = "s3cret-never-printed"

type ccServer struct {
	*httptest.Server
	mints atomic.Int32
	seen  atomic.Pointer[http.Request]
	form  atomic.Pointer[map[string]string]
}

func newCCServer(t *testing.T, tls bool, status int, expiresIn int) *ccServer {
	t.Helper()
	s := &ccServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": s.URL, "token_endpoint": s.URL + "/token"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		f := map[string]string{}
		for k := range r.PostForm {
			f[k] = r.PostForm.Get(k)
		}
		s.form.Store(&f)
		s.seen.Store(r)
		n := s.mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "no such client"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + string(rune('0'+n)), "token_type": "bearer", "expires_in": expiresIn})
	})
	if tls {
		s.Server = httptest.NewTLSServer(mux)
	} else {
		s.Server = httptest.NewServer(mux)
	}
	t.Cleanup(s.Close)
	return s
}

func secretFile(t *testing.T) CredentialStrategy {
	t.Helper()
	p := filepath.Join(t.TempDir(), "authelia-client-secret")
	require.NoError(t, os.WriteFile(p, []byte(ccSecret+"\n"), 0o400))
	return &File{Path: p, Required: true}
}

func TestClientCredentialsBasicAuthAudienceAndCache(t *testing.T) {
	srv := newCCServer(t, false, http.StatusOK, 300)
	now := time.Unix(1_000_000, 0)
	c := &ClientCredentials{
		TokenURL: srv.URL + "/token", ClientID: "figaro", Secret: secretFile(t),
		Audience: []string{"https://herald.kelliher.info"},
		Now:      func() time.Time { return now },
	}

	tok, err := c.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)
	id, sec, ok := srv.seen.Load().BasicAuth()
	assert.True(t, ok)
	assert.Equal(t, "figaro", id)
	assert.Equal(t, ccSecret, sec)
	form := *srv.form.Load()
	assert.Equal(t, "client_credentials", form["grant_type"])
	assert.Equal(t, "https://herald.kelliher.info", form["audience"])
	assert.NotContains(t, form, "client_secret", "basic auth must not also post the secret")

	tok, _ = c.Resolve()
	assert.Equal(t, "tok-1", tok)
	assert.EqualValues(t, 1, srv.mints.Load(), "a live token is reused")

	now = now.Add(241 * time.Second)
	tok, _ = c.Resolve()
	assert.Equal(t, "tok-2", tok, "a token within the skew of expiry is replaced")

	require.NoError(t, c.Invalidate("tok-2"))
	tok, _ = c.Resolve()
	assert.Equal(t, "tok-3", tok, "a rejected token is replaced")
}

func TestClientCredentialsSecretPost(t *testing.T) {
	srv := newCCServer(t, false, http.StatusOK, 300)
	c := &ClientCredentials{TokenURL: srv.URL + "/token", ClientID: "figaro", Secret: secretFile(t), AuthMethod: ClientSecretPost}
	_, err := c.Resolve()
	require.NoError(t, err)
	_, _, basic := srv.seen.Load().BasicAuth()
	assert.False(t, basic)
	form := *srv.form.Load()
	assert.Equal(t, "figaro", form["client_id"])
	assert.Equal(t, ccSecret, form["client_secret"])
}

// A refused mint names the server's error and never the secret.
func TestClientCredentialsErrorKeepsTheSecretOut(t *testing.T) {
	srv := newCCServer(t, false, http.StatusUnauthorized, 0)
	c := &ClientCredentials{TokenURL: srv.URL + "/token", ClientID: "figaro", Secret: secretFile(t)}
	_, err := c.Resolve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_client")
	assert.NotContains(t, err.Error(), ccSecret)
}

func TestClientCredentialsDiscoversTheTokenEndpoint(t *testing.T) {
	srv := newCCServer(t, true, http.StatusOK, 300)
	c := &ClientCredentials{Issuer: srv.URL, ClientID: "figaro", Secret: secretFile(t), HTTP: srv.Client()}
	tok, err := c.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)
	assert.Equal(t, srv.URL+"/token", c.TokenURL)
}

func TestClientCredentialsRefusesAPlainHTTPIssuer(t *testing.T) {
	c := &ClientCredentials{Issuer: "http://auth.example", ClientID: "figaro", Secret: secretFile(t)}
	_, err := c.Resolve()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "https"))
}

func TestClientCredentialsShortLivedTokenStillCachesBriefly(t *testing.T) {
	srv := newCCServer(t, false, http.StatusOK, 30)
	now := time.Unix(1_000_000, 0)
	c := &ClientCredentials{TokenURL: srv.URL + "/token", ClientID: "figaro", Secret: secretFile(t), Now: func() time.Time { return now }}
	c.Resolve()
	now = now.Add(10 * time.Second)
	c.Resolve()
	assert.EqualValues(t, 1, srv.mints.Load())
	now = now.Add(6 * time.Second)
	c.Resolve()
	assert.EqualValues(t, 2, srv.mints.Load())
}
