package auth

import (
	"fmt"
	"io"
	"net/http"
)

func SendRenewing(r TokenResolver, send func(token string) (*http.Response, error)) (*http.Response, error) {
	token, err := r.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve token: %w", err)
	}
	resp, err := send(token)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || token == "" {
		return resp, err
	}
	if r.Invalidate(token) != nil {
		return resp, nil
	}
	fresh, err := r.Resolve()
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP 401, and no fresh token: %w", err)
	}
	if fresh == token {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return send(fresh)
}

func Optional(r TokenResolver) TokenResolver { return optional{inner: r} }

type optional struct {
	inner TokenResolver
}

func (o optional) Resolve() (string, error) {
	tok, err := o.inner.Resolve()
	if err != nil {
		return "", nil
	}
	return tok, nil
}

func (o optional) Invalidate(token string) error { return o.inner.Invalidate(token) }
