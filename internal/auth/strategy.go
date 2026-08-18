package auth

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	hush "github.com/jack-work/hush/client"
)

// CredentialStrategy is one source of API credentials.
type CredentialStrategy interface {
	TryResolve() (token string, ok bool, err error)
	// Invalidate is called when a token is rejected (e.g. 401). A
	// non-nil error means invalidation itself failed (e.g. an OAuth
	// refresh was rejected).
	Invalidate(token string) error
}

// Aggregate is a TokenResolver that walks strategies in priority
// order. Re-evaluates on each call (picks up config changes).
type Aggregate struct {
	Strategies []CredentialStrategy
}

func (a *Aggregate) Resolve() (string, error) {
	var firstErr error
	for _, s := range a.Strategies {
		tok, ok, err := s.TryResolve()
		if ok {
			return tok, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return "", fmt.Errorf("no credential available; first strategy error: %w", firstErr)
	}
	return "", fmt.Errorf("no credential available (no strategy returned a token)")
}

func (a *Aggregate) Invalidate(token string) error {
	var errs []error
	for _, s := range a.Strategies {
		if err := s.Invalidate(token); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// EnvVar reads a token from an env var.
type EnvVar struct {
	Name string
}

func (e *EnvVar) TryResolve() (string, bool, error) {
	if e.Name == "" {
		return "", false, nil
	}
	v := os.Getenv(e.Name)
	if v == "" {
		return "", false, nil
	}
	return v, true, nil
}

func (*EnvVar) Invalidate(string) error { return nil }

// ConfigValue reads a plaintext token via a closure.
type ConfigValue struct {
	Get func() string
}

func (c *ConfigValue) TryResolve() (string, bool, error) {
	if c.Get == nil {
		return "", false, nil
	}
	v := c.Get()
	if v == "" {
		return "", false, nil
	}
	return v, true, nil
}

func (*ConfigValue) Invalidate(string) error { return nil }

// OAuth reads the access token for a named credential from the hush
// agent. Minting and renewal are the agent's responsibility - whether the
// credential is refreshed (OAuth) or exchanged (a Copilot session token);
// this strategy only fetches the current value and forces a mint on
// Invalidate so the next call sees a new token.
//
// One hush agent per machine means one token per credential, shared by
// every aria and every figaro process, renewed once. Nothing here caches:
// the socket round-trip IS the sharing.
type OAuth struct {
	Hush *hush.Client
	Name string

	// endpoint is the routing minted alongside the token, for credentials
	// whose exchange chooses the host (Copilot). Published atomically
	// because Resolve runs on provider goroutines and Endpoint is read by
	// whoever is about to build a request.
	endpoint atomic.Pointer[string]
}

func (o *OAuth) TryResolve() (string, bool, error) {
	if o.Hush == nil || o.Name == "" {
		return "", false, nil
	}
	tok, meta, err := o.Hush.OAuthGetFull(o.Name)
	if err != nil {
		if errors.Is(err, hush.ErrOAuthNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	o.rememberEndpoint(meta)
	return tok, true, nil
}

// Resolve makes OAuth usable as a TokenResolver on its own, which is what a
// provider whose bearer hush MINTS wants: there is no chain to walk, since
// no other source holds a bearer for it.
func (o *OAuth) Resolve() (string, error) {
	tok, ok, err := o.TryResolve()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no credential available for %q (hush holds none)", o.Name)
	}
	return tok, nil
}

// Endpoint is the API host the current token must be spent at, or "" when
// the credential says nothing about routing.
func (o *OAuth) Endpoint() string {
	if p := o.endpoint.Load(); p != nil {
		return *p
	}
	return ""
}

func (o *OAuth) rememberEndpoint(meta map[string]string) {
	if v := meta["api_base"]; v != "" {
		o.endpoint.Store(&v)
	}
}

func (o *OAuth) Invalidate(token string) error {
	if o.Hush == nil || o.Name == "" {
		return nil
	}
	_, meta, err := o.Hush.OAuthRefreshFull(o.Name)
	if err == nil {
		o.rememberEndpoint(meta)
		return nil
	}
	if errors.Is(err, hush.ErrOAuthRefreshPermanent) {
		return fmt.Errorf("oauth refresh for %q rejected (run: figaro login %s): %w", o.Name, o.Name, err)
	}
	return fmt.Errorf("oauth refresh for %q: %w", o.Name, err)
}

// EndpointCarrier is a resolver whose credential also names where its token
// must be spent. Copilot's exchange answers with an API host; nothing else
// does, so callers type-assert rather than widening TokenResolver.
type EndpointCarrier interface {
	Endpoint() string
}
