package anthropic

import (
	"context"
	"errors"
	"github.com/jack-work/figaro/internal/auth"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeResolver struct {
	tokens      []string
	invErr      error
	resolves    int
	invalidated []string
}

func (f *fakeResolver) Resolve() (string, error) {
	i := min(f.resolves, len(f.tokens)-1)
	f.resolves++
	return f.tokens[i], nil
}

func (f *fakeResolver) Invalidate(token string) error {
	f.invalidated = append(f.invalidated, token)
	return f.invErr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func always401() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
}

func TestDoWithAuthRetrySurfacesInvalidateError(t *testing.T) {
	refreshErr := errors.New(`oauth refresh for "anthropic" rejected (run: figaro provider login anthropic)`)
	fr := &fakeResolver{tokens: []string{"tok"}, invErr: refreshErr}
	a := &Anthropic{auth: fr, HTTPClient: always401()}

	_, _, err := a.doWithAuthRetry(context.Background(), func(apiKey string) (*http.Request, error) {
		return http.NewRequest("POST", "http://example.invalid", nil)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, refreshErr)
	assert.Contains(t, err.Error(), "invalidate failed")
	assert.Equal(t, []string{"tok"}, fr.invalidated)
}

func TestDoWithAuthRetryTokenUnchanged(t *testing.T) {
	fr := &fakeResolver{tokens: []string{"tok"}}
	a := &Anthropic{auth: fr, HTTPClient: always401()}

	_, _, err := a.doWithAuthRetry(context.Background(), func(apiKey string) (*http.Request, error) {
		return http.NewRequest("POST", "http://example.invalid", nil)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token unchanged after invalidate")
}

// Fixes #64: a grant hush calls dead stops the request with the login line,
// not a wrapped "invalidate failed" and not a second send.
func TestDoWithAuthRetryStopsAtLoginRequired(t *testing.T) {
	dead := &auth.LoginRequiredError{Credential: "anthropic", Cause: errors.New("invalid_grant")}
	fr := &fakeResolver{tokens: []string{"tok"}, invErr: dead}
	a := &Anthropic{auth: fr, HTTPClient: always401()}

	_, _, err := a.doWithAuthRetry(context.Background(), func(apiKey string) (*http.Request, error) {
		return http.NewRequest("POST", "http://example.invalid", nil)
	})
	require.ErrorIs(t, err, auth.ErrLoginRequired)
	assert.Equal(t, "anthropic: sign-in expired, run: figaro provider login anthropic", err.Error())
	assert.Equal(t, []string{"tok"}, fr.invalidated)
}

type unsignedResolver struct{}

func (unsignedResolver) Resolve() (string, error) {
	return "", &auth.NotSignedInError{Provider: "anthropic"}
}
func (unsignedResolver) Invalidate(string) error { return nil }

func TestARequestWithNoSignInSaysSoWithoutAResolvePrefix(t *testing.T) {
	a := &Anthropic{auth: unsignedResolver{}, HTTPClient: always401()}
	_, _, err := a.doWithAuthRetry(context.Background(), func(string) (*http.Request, error) {
		t.Fatal("built a request with no credential")
		return nil, nil
	})
	require.Error(t, err)
	assert.Equal(t, "no provider signed in: type :login, or run figaro provider login anthropic", err.Error())
}
