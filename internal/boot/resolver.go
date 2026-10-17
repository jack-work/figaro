package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	hush "github.com/jack-work/hush/client"
	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"

	_ "github.com/jack-work/figaro/internal/provider/anthropic"
	_ "github.com/jack-work/figaro/internal/provider/copilot"
	_ "github.com/jack-work/figaro/internal/provider/meta"
	_ "github.com/jack-work/figaro/internal/provider/routed"
)

func BuildResolver(loaded *config.Loaded, providerName string) (auth.TokenResolver, error) {
	reg := providerPkg.Lookup(providerName)
	if reg != nil && reg.ExchangeGrant != "" && reg.ExchangeURL != nil && reg.ExchangeURL(loaded) != "" {
		return &exchangeResolver{loaded: loaded, reg: reg, name: providerName}, nil
	}

	pf := &providerAuthFile{loaded: loaded, name: providerName}
	strategies := append(EnvironmentStrategies(reg), fileStrategies(pf)...)
	strategies = append(strategies, storedKey(pf)...)
	if reg != nil && reg.HasOAuth {
		strategies = append(strategies, &hushStrategy{build: func(c *hush.Client) auth.CredentialStrategy {
			return &auth.OAuth{Hush: c, Name: providerName}
		}})
	}
	return &auth.Aggregate{Strategies: strategies}, nil
}

func fileStrategies(pf *providerAuthFile) []auth.CredentialStrategy {
	out := []auth.CredentialStrategy{&configuredKeyFile{file: pf}}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		out = append(out, &auth.File{Path: filepath.Join(dir, pf.name)})
	}
	return out
}

type configuredKeyFile struct {
	file *providerAuthFile
}

func (c *configuredKeyFile) TryResolve() (string, bool, error) {
	pa, err := c.file.load()
	if err != nil || pa.APIKeyFile == "" {
		return "", false, err
	}
	path := pa.APIKeyFile
	if !filepath.IsAbs(path) {
		dir := os.Getenv("CREDENTIALS_DIRECTORY")
		if dir == "" {
			return "", false, fmt.Errorf("%s: api_key_file %q is relative and $CREDENTIALS_DIRECTORY is unset", c.file.path(), path)
		}
		path = filepath.Join(dir, path)
	}
	return (&auth.File{Path: path, Required: true}).TryResolve()
}

func (*configuredKeyFile) Invalidate(string) error { return nil }

func storedKey(pf *providerAuthFile) []auth.CredentialStrategy {
	return []auth.CredentialStrategy{
		&auth.ConfigValue{Get: func() string {
			pa, _ := pf.load()
			if strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
				return ""
			}
			return pa.APIKey
		}},
		&encryptedAPIKey{Hush: strategyHushClient, File: pf},
	}
}

func EnvironmentStrategies(reg *providerPkg.Registration) []auth.CredentialStrategy {
	if reg == nil {
		return nil
	}
	names := reg.CredentialEnvs()
	strategies := make([]auth.CredentialStrategy, 0, len(names))
	for _, name := range names {
		if name != "" {
			strategies = append(strategies, &auth.EnvVar{Name: name})
		}
	}
	return strategies
}

type hushStrategy struct {
	build func(*hush.Client) auth.CredentialStrategy

	mu    sync.Mutex
	inner auth.CredentialStrategy
}

func (s *hushStrategy) get(construct bool) (auth.CredentialStrategy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inner != nil || !construct {
		return s.inner, nil
	}
	c, err := strategyHushClient()
	if err != nil {
		return nil, err
	}
	s.inner = s.build(c)
	return s.inner, nil
}

func (s *hushStrategy) TryResolve() (string, bool, error) {
	inner, err := s.get(true)
	if err != nil {
		return "", false, err
	}
	return inner.TryResolve()
}

func (s *hushStrategy) Invalidate(token string) error {
	inner, _ := s.get(false)
	if inner == nil {
		return nil
	}
	return inner.Invalidate(token)
}

type exchangeResolver struct {
	loaded *config.Loaded
	reg    *providerPkg.Registration
	name   string

	mu    sync.Mutex
	oauth *auth.OAuth
}

func (e *exchangeResolver) ready() (*auth.OAuth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.oauth != nil {
		return e.oauth, nil
	}
	h, err := strategyHush()
	if err != nil {
		return nil, err
	}
	if err := ensureExchangeCredential(e.loaded, h, e.reg, e.name); err != nil {
		return nil, err
	}
	e.oauth = &auth.OAuth{Hush: h.Client(), Name: e.name}
	return e.oauth, nil
}

func (e *exchangeResolver) Resolve() (string, error) {
	o, err := e.ready()
	if err != nil {
		return "", err
	}
	return o.Resolve()
}

func (e *exchangeResolver) Invalidate(token string) error {
	e.mu.Lock()
	o := e.oauth
	e.mu.Unlock()
	if o == nil {
		return nil
	}
	return o.Invalidate(token)
}

func (e *exchangeResolver) Endpoint() string {
	e.mu.Lock()
	o := e.oauth
	e.mu.Unlock()
	if o == nil {
		return ""
	}
	return o.Endpoint()
}

var exchangeSetupMu sync.Mutex

// ensureExchangeCredential hands the durable secret to hush under an exchange
// grant, once.
func ensureExchangeCredential(loaded *config.Loaded, mh *managed.Hush, reg *providerPkg.Registration, name string) error {
	exchangeSetupMu.Lock()
	defer exchangeSetupMu.Unlock()

	h := mh.Client()
	if h == nil || reg == nil || reg.ExchangeGrant == "" {
		return nil
	}
	tokenURL := ""
	if reg.ExchangeURL != nil {
		tokenURL = reg.ExchangeURL(loaded)
	}
	if tokenURL == "" {
		return nil
	}

	// The agent may predate this binary; hush replaces its own child.
	if err := mh.EnsureGrant(reg.ExchangeGrant); err != nil {
		return err
	}

	names, err := h.OAuthList()
	if err != nil {
		return fmt.Errorf("hush: list credentials: %w", err)
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}

	durable, source := durableSecretFor(loaded, h, reg, name)
	if durable == "" {
		return nil
	}

	if err := h.OAuthRegister(hush.OAuthRegisterRequest{
		Name:         name,
		TokenURL:     tokenURL,
		Grant:        reg.ExchangeGrant,
		RefreshToken: durable,
	}); err != nil {
		return fmt.Errorf("hush: register %s credential from %s: %w", name, source, err)
	}
	fmt.Fprintf(os.Stderr, "figaro: handed the %s credential (%s) to hush; it owns the session token now\n", name, source)
	return nil
}

func durableSecretFor(loaded *config.Loaded, h *hush.Client, reg *providerPkg.Registration, name string) (secret, source string) {
	for _, env := range reg.CredentialEnvs() {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, "$" + env
		}
	}
	for _, f := range fileStrategies(&providerAuthFile{loaded: loaded, name: name}) {
		if v, ok, _ := f.TryResolve(); ok {
			return v, "a credential file"
		}
	}

	var pa config.ProviderAuth
	if err := loaded.LoadProviderAuth(name, &pa); err != nil || pa.APIKey == "" {
		return "", ""
	}
	path := loaded.ProviderAuthPath(name)
	if !strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
		return pa.APIKey, path
	}
	dec, err := h.Decrypt(map[string]string{"api_key": pa.APIKey})
	if err != nil {
		return "", ""
	}
	return dec["api_key"], path
}
