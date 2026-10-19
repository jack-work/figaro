package auth

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const subjectToken = "kanidm-api-token-never-printed"

func subjectFile(t *testing.T, value string) (CredentialStrategy, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rou2-api-token")
	require.NoError(t, os.WriteFile(p, []byte(value+"\n"), 0o400))
	return &File{Path: p, Required: true}, p
}

func TestTokenExchangeIsAPublicClientPostingTheSubject(t *testing.T) {
	srv := newCCServer(t, false, http.StatusOK, 900)
	subject, _ := subjectFile(t, subjectToken)
	x := &TokenExchange{
		TokenURL: srv.URL + "/token", ClientID: "rou2", Subject: subject,
		Scopes: []string{"openid", "groups", "profile"},
	}

	tok, err := x.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)

	form := *srv.form.Load()
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", form["grant_type"])
	assert.Equal(t, "rou2", form["client_id"])
	assert.Equal(t, subjectToken, form["subject_token"], "the subject travels in the body")
	assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", form["subject_token_type"])
	assert.Equal(t, "openid groups profile", form["scope"])
	assert.NotContains(t, form, "client_secret", "a public client sends no secret")
	assert.NotContains(t, form, "audience", "no audience unless configured")
	_, _, basic := srv.seen.Load().BasicAuth()
	assert.False(t, basic, "a public client sends no basic auth")
}

func TestTokenExchangeCachesThenReExchanges(t *testing.T) {
	srv := newCCServer(t, false, http.StatusOK, 900)
	subject, _ := subjectFile(t, subjectToken)
	now := time.Unix(1_000_000, 0)
	x := &TokenExchange{
		TokenURL: srv.URL + "/token", ClientID: "rou2", Subject: subject,
		Now: func() time.Time { return now },
	}

	tok, _ := x.Resolve()
	assert.Equal(t, "tok-1", tok)
	tok, _ = x.Resolve()
	assert.Equal(t, "tok-1", tok)
	assert.EqualValues(t, 1, srv.mints.Load(), "a live token is reused")

	now = now.Add(841 * time.Second)
	tok, _ = x.Resolve()
	assert.Equal(t, "tok-2", tok, "a 900 s token is re-exchanged inside the skew")

	require.NoError(t, x.Invalidate("tok-2"))
	tok, _ = x.Resolve()
	assert.Equal(t, "tok-3", tok, "a rejected token is re-exchanged")
}

func TestTokenExchangeReadsARotatedSubject(t *testing.T) {
	srv := newCCServer(t, false, http.StatusOK, 900)
	subject, path := subjectFile(t, "first")
	x := &TokenExchange{TokenURL: srv.URL + "/token", ClientID: "rou2", Subject: subject}

	_, err := x.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "first", (*srv.form.Load())["subject_token"])

	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("second\n"), 0o400))
	tok, _ := x.Resolve()
	require.NoError(t, x.Invalidate(tok))
	_, err = x.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "second", (*srv.form.Load())["subject_token"], "the subject is read per exchange, not once")
}

func TestTokenExchangeErrorKeepsTheSubjectOut(t *testing.T) {
	srv := newCCServer(t, false, http.StatusBadRequest, 0)
	subject, _ := subjectFile(t, subjectToken)
	x := &TokenExchange{TokenURL: srv.URL + "/token", ClientID: "rou2", Subject: subject}

	_, err := x.Resolve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 400")
	assert.NotContains(t, err.Error(), subjectToken)
}

func TestTokenExchangeDiscoversTheTokenEndpoint(t *testing.T) {
	srv := newCCServer(t, true, http.StatusOK, 900)
	subject, _ := subjectFile(t, subjectToken)
	x := &TokenExchange{Issuer: srv.URL, ClientID: "rou2", Subject: subject, HTTP: srv.Client()}

	tok, err := x.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)
	assert.Equal(t, srv.URL+"/token", x.TokenURL)
}

func TestTokenExchangeRefusesAMissingSubject(t *testing.T) {
	x := &TokenExchange{TokenURL: "https://idm.invalid/token", ClientID: "rou2",
		Subject: &File{Path: filepath.Join(t.TempDir(), "absent"), Required: true}}
	_, err := x.Resolve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subject token")
}
