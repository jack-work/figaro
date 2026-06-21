package auth

import (
	"errors"
	"fmt"
	"os"

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
// agent. Refresh is the agent's responsibility; this strategy only
// fetches the current value and forces a refresh on Invalidate so the
// next call sees a new token.
type OAuth struct {
	Hush *hush.Client
	Name string
}

func (o *OAuth) TryResolve() (string, bool, error) {
	if o.Hush == nil || o.Name == "" {
		return "", false, nil
	}
	tok, err := o.Hush.OAuthGet(o.Name)
	if err != nil {
		if errors.Is(err, hush.ErrOAuthNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return tok, true, nil
}

func (o *OAuth) Invalidate(token string) error {
	if o.Hush == nil || o.Name == "" {
		return nil
	}
	_, err := o.Hush.OAuthRefresh(o.Name)
	if err == nil {
		return nil
	}
	if errors.Is(err, hush.ErrOAuthRefreshPermanent) {
		return fmt.Errorf("oauth refresh for %q rejected (run: figaro login %s): %w", o.Name, o.Name, err)
	}
	return fmt.Errorf("oauth refresh for %q: %w", o.Name, err)
}
