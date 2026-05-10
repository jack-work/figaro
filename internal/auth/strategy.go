package auth

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	hush "github.com/jack-work/hush/client"
)

// CredentialStrategy is one source of API credentials. The Aggregate
// walks strategies in priority order and the first to return ok=true
// wins. ok=false with err==nil means "I have nothing right now, try
// the next strategy". A non-nil err means the strategy believes it
// owns the credential but failed to produce one.
type CredentialStrategy interface {
	TryResolve() (token string, ok bool, err error)
	// Invalidate is called when an upstream API rejects a token
	// (typically a 401). Cache-bearing strategies must clear the
	// matching cached value.
	Invalidate(token string)
}

// Aggregate is a TokenResolver backed by a priority-ordered list of
// strategies. Each Resolve walks the list until one returns ok=true.
// Lazy + adaptive: each call re-evaluates every strategy, so env
// vars, config edits, and new on-disk credentials are picked up
// without a restart.
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

// EnvVar reads a token from a process environment variable on every
// call. Empty → skip.
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

// ConfigValue reads a plaintext token via a closure each call. The
// closure is the wiring layer's hook to re-read provider config —
// adaptivity is the closure's responsibility (typically: re-parse
// config.toml).
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

// EncryptedConfig reads a hush-encrypted secret from a file. The
// file content is the ciphertext (no TOML wrapper); leading/trailing
// whitespace is trimmed. Decrypted plaintext is mtime-cached so
// repeat calls don't re-decrypt the same bytes.
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

// OAuth bridges TokenManager into the strategy interface. "Has
// credential" is decided by whether the auth file exists; once it
// does, the manager's expiry/refresh logic takes over.
type OAuth struct {
	Manager *TokenManager
}

func (o *OAuth) TryResolve() (string, bool, error) {
	if o.Manager == nil {
		return "", false, nil
	}
	if _, err := os.Stat(o.Manager.filePath); os.IsNotExist(err) {
		return "", false, nil
	}
	tok, err := o.Manager.AccessToken()
	if err != nil {
		return "", false, err
	}
	return tok, true, nil
}

func (o *OAuth) Invalidate(token string) {
	if o.Manager != nil {
		o.Manager.Invalidate(token)
	}
}
