package anthropicsdk

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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

func unauthorizedErr() error {
	req, _ := http.NewRequest("POST", "https://api.example.invalid/v1/messages", nil)
	return &anthropic.Error{StatusCode: 401, Request: req, Response: &http.Response{StatusCode: 401}}
}

func TestCallWithAuthRetrySurfacesInvalidateError(t *testing.T) {
	refreshErr := errors.New(`oauth refresh for "anthropic" rejected (run: figaro login anthropic)`)
	fr := &fakeResolver{tokens: []string{"tok"}, invErr: refreshErr}
	p := &Provider{resolver: fr}

	err := p.callWithAuthRetry(context.Background(), func([]option.RequestOption) error {
		return unauthorizedErr()
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, refreshErr)
	assert.Contains(t, err.Error(), "invalidate failed")
	assert.Equal(t, []string{"tok"}, fr.invalidated)
}

func TestCallWithAuthRetryTokenUnchanged(t *testing.T) {
	fr := &fakeResolver{tokens: []string{"tok"}}
	p := &Provider{resolver: fr}

	err := p.callWithAuthRetry(context.Background(), func([]option.RequestOption) error {
		return unauthorizedErr()
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token unchanged after invalidate")
}

func TestCallWithAuthRetryRetriesWithFreshToken(t *testing.T) {
	fr := &fakeResolver{tokens: []string{"old", "new"}}
	p := &Provider{resolver: fr}

	calls := 0
	err := p.callWithAuthRetry(context.Background(), func([]option.RequestOption) error {
		calls++
		if calls == 1 {
			return unauthorizedErr()
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []string{"old"}, fr.invalidated)
}
