package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	hush "github.com/jack-work/hush/client"
)

// CredentialStrategy is one source of API credentials.
type CredentialStrategy interface {
	TryResolve() (token string, ok bool, err error)
	// Invalidate is called when a token is rejected (e.g. 401).
	Invalidate(token string)
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

func (a *Aggregate) Invalidate(token string) {
	for _, s := range a.Strategies {
		s.Invalidate(token)
	}
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

func (*EnvVar) Invalidate(string) {}

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

func (*ConfigValue) Invalidate(string) {}

// EncryptedConfig reads a hush-encrypted secret from a file.
// Mtime-cached to avoid re-decrypting.
type EncryptedConfig struct {
	Hush *hush.Client
	Path string

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

func (e *EncryptedConfig) TryResolve() (string, bool, error) {
	if e.Hush == nil || e.Path == "" {
		return "", false, nil
	}
	info, err := os.Stat(e.Path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", e.Path, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached != "" && info.ModTime().Equal(e.cachedAt) {
		return e.cached, true, nil
	}
	data, err := os.ReadFile(e.Path)
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", e.Path, err)
	}
	cipher := strings.TrimSpace(string(data))
	if cipher == "" {
		return "", false, nil
	}
	res, err := e.Hush.Decrypt(map[string]string{"v": cipher})
	if err != nil {
		return "", false, fmt.Errorf("hush decrypt %s: %w", e.Path, err)
	}
	plain := res["v"]
	e.cached = plain
	e.cachedAt = info.ModTime()
	return plain, true, nil
}

func (e *EncryptedConfig) Invalidate(token string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached == token {
		e.cached = ""
		e.cachedAt = time.Time{}
	}
}

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

func (o *OAuth) Invalidate(token string) {
	if o.Hush == nil || o.Name == "" {
		return
	}
	_, err := o.Hush.OAuthRefresh(o.Name)
	if err != nil && errors.Is(err, hush.ErrOAuthRefreshPermanent) {
		fmt.Fprintf(os.Stderr, "OAuth refresh for %q rejected. Run: figaro login %s\n", o.Name, o.Name)
	}
}
