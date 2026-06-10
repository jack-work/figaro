package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolver returns tokens in sequence (repeating the last) and a
// canned Invalidate error.
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
	refreshErr := errors.New(`oauth refresh for "anthropic" rejected (run: figaro login anthropic)`)
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
