package boot

import (
	"fmt"
	"os"
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

// BuildResolver resolves in order: env, plaintext api_key, encrypted api_key, OAuth.
func BuildResolver(loaded *config.Loaded, providerName string) (auth.TokenResolver, error) {
	h, err := Hush()
	if err != nil {
		return nil, err
	}
	hushClient := h.Client()

	reg := providerPkg.Lookup(providerName)
	hasOAuth := false
	if reg != nil {
		hasOAuth = reg.HasOAuth
	}

	// An exchanged bearer has one source, hush; the env var and api_key are
	// the durable secret, not a bearer.
	if reg != nil && reg.ExchangeGrant != "" && reg.ExchangeURL != nil && reg.ExchangeURL(loaded) != "" {
		if err := ensureExchangeCredential(loaded, h, reg, providerName); err != nil {
			return nil, err
		}
		// Bare, not an Aggregate: the provider reads the routing off it.
		return &auth.OAuth{Hush: hushClient, Name: providerName}, nil
	}

	strategies := append(EnvironmentStrategies(reg), storedKey(loaded, hushClient, providerName)...)
	if hasOAuth {
		strategies = append(strategies, &auth.OAuth{
			Hush: hushClient,
			Name: providerName,
		})
	}
	return &auth.Aggregate{Strategies: strategies}, nil
}

func BorrowedResolver(loaded *config.Loaded, provider, name string, envs []string) (auth.TokenResolver, error) {
	if provider == "" {
		return KeyResolver(loaded, name, envs)
	}
	if providerPkg.Lookup(provider) == nil {
		return nil, fmt.Errorf("%s: provider = %q is not a provider (have: %s)", name, provider, strings.Join(providerPkg.Names(), ", "))
	}
	return BuildResolver(loaded, provider)
}

func KeyResolver(loaded *config.Loaded, name string, envs []string) (auth.TokenResolver, error) {
	h, err := Hush()
	if err != nil {
		return nil, err
	}
	var strategies []auth.CredentialStrategy
	for _, env := range envs {
		strategies = append(strategies, &auth.EnvVar{Name: env})
	}
	strategies = append(strategies, storedKey(loaded, h.Client(), name)...)
	return &auth.Aggregate{Strategies: strategies}, nil
}

func storedKey(loaded *config.Loaded, hushClient *hush.Client, name string) []auth.CredentialStrategy {
	return []auth.CredentialStrategy{
		&auth.ConfigValue{Get: func() string {
			var pa config.ProviderAuth
			_ = loaded.LoadProviderAuth(name, &pa)
			if pa.APIKey == "" || strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
				return ""
			}
			return pa.APIKey
		}},
		&encryptedAPIKey{Hush: hushClient, ConfigPath: loaded.ProviderAuthPath(name)},
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
