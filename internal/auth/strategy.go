package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"

	hush "github.com/jack-work/hush/client"
)

type CredentialStrategy interface {
	TryResolve() (token string, ok bool, err error)
	// Invalidate: a non-nil error means invalidation itself failed.
	Invalidate(token string) error
}

// Aggregate walks strategies in priority order on every call.
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

type File struct {
	Path     string
	Required bool
}

func (f *File) TryResolve() (string, bool, error) {
	if f.Path == "" {
		return "", false, nil
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !f.Required {
			return "", false, nil
		}
		return "", false, err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		if f.Required {
			return "", false, fmt.Errorf("credential file %s is empty", f.Path)
		}
		return "", false, nil
	}
	return v, true, nil
}

func (*File) Invalidate(string) error { return nil }

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

// OAuth fetches a named credential from the hush agent, which owns minting
// and renewal; Invalidate forces a mint.
type OAuth struct {
	Hush *hush.Client
	Name string

	// endpoint is published atomically: Resolve runs on provider goroutines.
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

// Resolve makes OAuth a TokenResolver on its own, for a provider whose bearer
// only hush mints.
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

// Endpoint is the API host the current token must be spent at, or "".
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

// EndpointCarrier is a resolver whose credential also names the API host.
// Only Copilot's exchange does, so callers type-assert.
type EndpointCarrier interface {
	Endpoint() string
}
